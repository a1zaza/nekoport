package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
)

type App struct {
	idleFocused      map[string]string
	imapUsage        map[string]*imapUsage
	idleLimits       map[string]int
	idleTotals       map[string]int
	idleUnwatched    map[string][]string
	syncTurn         map[string]bool
	idleWatchers     map[string]*idleWatcher
	idleAccounts     map[string]int
	idlePending      map[string]idleDemand
	notifyTimer      *time.Timer
	lastNotification time.Time
	taskCancels      map[string]context.CancelFunc
	delivery         map[string]bool
	pending          map[string]bool
	workerGate       chan struct{}
	Store            *storage.Store
	Vault            *secrets.Vault
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	mu               sync.Mutex
	running          map[string]bool
	listeners        map[chan string]bool
	contentGate      chan struct{}
}

func New(s *storage.Store, v *secrets.Vault) *App {
	ctx, c := context.WithCancel(context.Background())
	return &App{idleFocused: map[string]string{}, imapUsage: map[string]*imapUsage{}, idleLimits: map[string]int{}, idleTotals: map[string]int{}, idleUnwatched: map[string][]string{}, syncTurn: map[string]bool{}, idleWatchers: map[string]*idleWatcher{}, idleAccounts: map[string]int{}, idlePending: map[string]idleDemand{}, taskCancels: map[string]context.CancelFunc{}, delivery: map[string]bool{}, Store: s, Vault: v, ctx: ctx, cancel: c, running: map[string]bool{}, listeners: map[chan string]bool{}, contentGate: make(chan struct{}, 2), pending: map[string]bool{}, workerGate: make(chan struct{}, 2)}
}
func (a *App) Start() { // Any interrupted SMTP attempt must be confirmed manually, never replayed.
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for id, j := range s.Jobs {
			if j.State == "sending" || j.State == "submitting" {
				j.State = "unknown"
				j.Error = "上次发送被中断，结果待确认，请核对邮箱后处理"
				ops = append(ops, storage.Put("jobs", id, j))
			}
		}
		for id, j := range s.Jobs {
			if j.State == "sent" && j.Archive == "submitting" {
				j.Archive = "unknown"
				j.Error = "上次归档被中断，请先在服务器核对，不会自动重新归档"
				ops = append(ops, storage.Put("jobs", id, j))
			}
		}
		for id, o := range s.Operations {
			if o.State == "running" {
				o.State = "failed"
				o.Error = "上次操作被中断，请同步核对后重试"
				ops = append(ops, storage.Put("operations", id, o))
				if o.TargetUIDFloor > 0 {
					source := s.Messages[o.MessageID].MailboxID
					var folders []domain.SyncFolder
					if source != "" {
						folders = append(folders, domain.SyncFolder{MailboxID: source, NewMail: true, Membership: true})
					}
					if o.Target != "" {
						folders = append(folders, domain.SyncFolder{MailboxID: o.Target, NewMail: true, ActivityFrom: o.TargetUIDFloor, ActivityValidity: o.TargetUIDValidity})
					}
					ops = append(ops, deltaOps(s, o.AccountID, "operation:move-recovery", folders, false, false)...)
				}
			}
		}
		return ops, nil
	})
	a.recoverSyncTasks()
	a.wg.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		maintenance := time.NewTicker(10 * time.Minute)
		defer maintenance.Stop()
		a.maintainIdle(time.Now().UTC())
		a.scheduleSync(time.Now().UTC())
		for {
			select {
			case <-a.ctx.Done():
				return
			case now := <-ticker.C:
				a.maintainIdle(now.UTC())
				a.scheduleSync(now.UTC())
			case <-maintenance.C:
				_ = a.Store.Checkpoint()
				_ = a.Store.Collect()
			}
		}
	})
}
func (a *App) Close() {
	a.cancel()
	a.wg.Wait()
	a.mu.Lock()
	if a.notifyTimer != nil {
		a.notifyTimer.Stop()
		a.notifyTimer = nil
	}
	a.mu.Unlock()
}
func (a *App) Subscribe() (chan string, func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := make(chan string, 1)
	a.listeners[c] = true
	return c, func() { a.mu.Lock(); delete(a.listeners, c); a.mu.Unlock() }
}
func (a *App) Changed() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil || a.notifyTimer != nil {
		return
	}
	elapsed := time.Since(a.lastNotification)
	if elapsed < time.Second {
		a.notifyTimer = time.AfterFunc(time.Second-elapsed, func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.notifyTimer = nil
			if a.ctx.Err() != nil {
				return
			}
			a.notifyLocked()
		})
		return
	}
	a.notifyLocked()
}
func (a *App) notifyLocked() {
	a.lastNotification = time.Now()
	for c := range a.listeners {
		select {
		case c <- "changed":
		default:
		}
	}
}
func (a *App) Running() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := map[string]bool{}
	for k, b := range a.running {
		v[k] = b
	}
	return v
}
func (a *App) SyncAll() {
	var ids []string
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for id, x := range s.Accounts {
			if !x.Paused {
				ids = append(ids, id)
			}
		}
	})
	for _, id := range ids {
		_, _ = a.CreateSyncTask(id, "quick", "manual", "")
	}
}
func (a *App) Trigger(id string) { a.kick(id, true) }
func (a *App) kick(id string, delivery bool) {
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	if delivery {
		a.delivery[id] = true
	}
	if a.running[id] {
		a.pending[id] = true
		a.mu.Unlock()
		return
	}
	a.running[id] = true
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		defer func() {
			a.mu.Lock()
			again := a.pending[id]
			delete(a.pending, id)
			delete(a.running, id)
			a.mu.Unlock()
			a.Changed()
			if again {
				a.kick(id, false)
			}
		}()
		select {
		case a.workerGate <- struct{}{}:
			defer func() { <-a.workerGate }()
		case <-a.ctx.Done():
			return
		}
		a.Changed()
		a.work(id)
	}()
}
func (a *App) account(id string) (domain.Account, secrets.Credentials, error) {
	var x domain.Account
	var ok bool
	a.Store.Read(func(s domain.State, _ storage.Index) { x, ok = s.Accounts[id] })
	if !ok {
		return x, secrets.Credentials{}, errors.New("邮箱不存在")
	}
	c, e := a.Vault.Get(x.CredentialRef)
	return x, c, e
}
func (a *App) SaveAccount(x domain.Account, imapPass, smtpPass string) (domain.Account, error) {
	if len(x.Name) == 0 || len(x.Name) > 120 || len(x.Address) > 320 || len(imapPass) > 4096 || len(smtpPass) > 4096 {
		return x, errors.New("邮箱名称或登录信息长度无效")
	}
	if _, e := mail.ParseAddress(x.Address); e != nil {
		return x, errors.New("邮箱地址无效")
	}
	if e := mailer.ValidateEndpoint(x.IMAP); e != nil {
		return x, e
	}
	if e := mailer.ValidateEndpoint(x.SMTP); e != nil {
		return x, e
	}
	if x.SyncDays < 1 || x.SyncDays > 3650 {
		return x, errors.New("同步天数范围为 1–3650")
	}
	var old domain.Account
	if x.ID != "" {
		var e error
		old, _, e = a.account(x.ID)
		if e != nil {
			return x, e
		}
		if old.Revision != x.Revision {
			return x, storage.ErrConflict
		}
	}
	var c secrets.Credentials
	if old.ID != "" {
		var e error
		c, e = a.Vault.Get(old.CredentialRef)
		if e != nil {
			return x, e
		}
	}
	if imapPass != "" {
		c.IMAP = imapPass
	}
	if smtpPass != "" {
		c.SMTP = smtpPass
	}
	if c.IMAP == "" || c.SMTP == "" {
		return x, errors.New("请填写接收和发送密码")
	}
	ref, e := a.Vault.Put(c)
	if e != nil {
		return x, e
	}
	if x.ID == "" {
		x.ID = storage.ID()
	}
	if x.SyncScope == "" {
		x.SyncScope = old.Scope()
	}
	if x.SyncScope != "recent" && x.SyncScope != "all" {
		return x, errors.New("同步范围无效")
	}
	if x.ReconcileIntervalMinutes == 0 {
		x.ReconcileIntervalMinutes = old.ReconcileMinutes()
	}
	if x.ReconcileIntervalMinutes < 1 || x.ReconcileIntervalMinutes > 1440 {
		return x, errors.New("对齐间隔范围为 1–1440 分钟")
	}
	if x.ReconcileEnabled == nil {
		x.ReconcileEnabled = old.ReconcileEnabled
	}
	if x.IMAPMaxConnections == 0 {
		x.IMAPMaxConnections = imapLimit(old)
	}
	if x.IMAPMaxConnections < 4 || x.IMAPMaxConnections > 50 {
		return x, errors.New("IMAP 连接上限范围为 4–50")
	}
	x.CredentialRef = ref
	x.Revision++
	x.Error = ""
	e = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		if _, exists := s.Accounts[x.ID]; !exists && len(s.Accounts) >= 20 {
			return nil, errors.New("个人实例最多配置 20 个邮箱")
		}
		current, exists := s.Accounts[x.ID]
		if exists && current.Revision != old.Revision {
			return nil, storage.ErrConflict
		}
		status := s.SyncStatus[x.ID]
		status.AccountID = x.ID
		status.ErrorKind = ""
		status.Error = ""
		status.RetryAt = time.Time{}
		status.Failures = 0
		status.NextQuick = time.Time{}
		status.NextFull = time.Time{}
		ops := []storage.Op{storage.Put("accounts", x.ID, x), storage.Put("syncStatus", x.ID, status)}
		if x.Paused {
			for id, t := range s.SyncTasks {
				if t.AccountID == x.ID && t.Active() {
					t.State = "paused"
					t.UpdatedAt = time.Now().UTC()
					ops = append(ops, storage.Put("syncTasks", id, t))
				}
			}
		}
		return ops, nil
	})
	if e == nil {
		a.Changed()
		a.Trigger(x.ID)
	}
	return x, e
}
func (a *App) RemoveAccount(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running[id] {
		return errors.New("邮箱正在同步，请等待结束后再移除")
	}
	return a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		if _, ok := s.Accounts[id]; !ok {
			return nil, errors.New("邮箱不存在")
		}
		ops := []storage.Op{storage.Del("accounts", id), storage.Del("syncStatus", id)}
		for tid, t := range s.SyncTasks {
			if t.AccountID == id {
				ops = append(ops, storage.Del("syncTasks", tid))
			}
		}
		for k, v := range s.Mailboxes {
			if v.AccountID == id {
				ops = append(ops, storage.Del("mailboxes", k))
			}
		}
		for k, v := range s.Messages {
			if v.AccountID == id {
				ops = append(ops, storage.Del("messages", k))
			}
		}
		for k, v := range s.Drafts {
			if v.AccountID == id {
				ops = append(ops, storage.Del("drafts", k))
			}
		}
		for k, v := range s.Jobs {
			if v.AccountID == id && (v.State == "queued" || v.State == "unknown") {
				return nil, errors.New("请先处理此邮箱的待发送或结果待确认任务")
			}
			if v.AccountID == id {
				ops = append(ops, storage.Del("jobs", k))
			}
		}
		for k, v := range s.Operations {
			if v.AccountID == id {
				ops = append(ops, storage.Del("operations", k))
			}
		}
		return ops, nil
	})
}
func (a *App) work(id string) {
	x, c, e := a.account(id)
	if e != nil || x.Paused {
		return
	}
	a.mu.Lock()
	deliver := a.delivery[id]
	delete(a.delivery, id)
	a.mu.Unlock()
	if deliver {
		a.sendJobs(x, c)
		a.processOperations(x, c)
	}
	if a.ctx.Err() != nil {
		return
	}
	if a.advanceSyncTask(x, c) {
		a.kick(id, false)
	}
}

func cleanError(e error) string {
	if e == nil {
		return ""
	}
	s := e.Error()
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}
func role(name string, attrs []string) string {
	for _, r := range []string{"Sent", "Trash", "Junk", "Drafts", "Archive"} {
		for _, a := range attrs {
			if strings.EqualFold(a, "\\"+r) {
				return strings.ToLower(r)
			}
		}
	}
	n := strings.ToLower(name)
	switch n {
	case "inbox":
		return "inbox"
	case "sent", "sent messages", "已发送":
		return "sent"
	case "trash", "deleted messages", "已删除":
		return "trash"
	case "junk", "spam", "垃圾邮件":
		return "junk"
	case "drafts", "草稿":
		return "drafts"
	case "archive", "归档":
		return "archive"
	}
	return "folder"
}
func (a *App) Content(id string) (domain.Message, domain.Content, error) {
	return a.ContentContext(a.ctx, id)
}
func (a *App) ContentContext(ctx context.Context, id string) (domain.Message, domain.Content, error) {
	select {
	case a.contentGate <- struct{}{}:
		defer func() { <-a.contentGate }()
	case <-ctx.Done():
		return domain.Message{}, domain.Content{}, ctx.Err()
	}

	var m domain.Message
	var ok bool
	a.Store.Read(func(s domain.State, _ storage.Index) { m, ok = s.Messages[id] })
	if !ok {
		return m, domain.Content{}, errors.New("邮件不存在")
	}
	if m.ContentID != "" {
		b, e := a.Store.LoadObject(m.ContentID)
		if e != nil {
			return m, domain.Content{}, e
		}
		var content domain.Content
		e = json.Unmarshal(b, &content)
		if e != nil {
			return m, content, e
		}
		if content.HTML != "" && content.HTMLVersion < mailer.HTMLVersion && m.RawID != "" {
			raw, err := a.Store.LoadObject(m.RawID)
			if err != nil {
				return m, content, err
			}
			restored, err := mailer.Parse(raw, a.Store.Object)
			if err != nil {
				return m, content, err
			}
			content.HTML = restored.HTML
			content.HTMLVersion = restored.HTMLVersion
			updated, err := json.Marshal(content)
			if err != nil {
				return m, content, err
			}
			contentID, err := a.Store.Object(updated)
			if err != nil {
				return m, content, err
			}
			err = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				current, exists := s.Messages[id]
				if !exists || current.ContentID != m.ContentID {
					return nil, storage.ErrConflict
				}
				current.ContentID = contentID
				current.CacheBytes += int64(len(updated) - len(b))
				current.Revision++
				m = current
				return []storage.Op{storage.Put("messages", id, current)}, nil
			})
			if err != nil {
				return m, content, err
			}
		}
		return m, content, nil
	}
	if m.Size > mailer.MaxMIME {
		return m, domain.Content{}, errors.New("邮件超过 30 MiB，暂不下载正文")
	}
	x, cred, e := a.account(m.AccountID)
	if e != nil {
		return m, domain.Content{}, e
	}
	var box domain.Mailbox
	a.Store.Read(func(s domain.State, _ storage.Index) { box = s.Mailboxes[m.MailboxID] })
	c, e := a.dialIMAP(ctx, x, cred.IMAP)
	if e != nil {
		return m, domain.Content{}, e
	}
	defer c.Terminate()
	status, e := c.Select(box.Name, true)
	if e != nil {
		return m, domain.Content{}, e
	}
	if status.UidValidity != m.UIDValidity {
		return m, domain.Content{}, errors.New("邮箱标识已变化，请先同步")
	}
	set := new(imap.SeqSet)
	set.AddNum(m.UID)
	section := &imap.BodySectionName{Peek: true}
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(set, []imap.FetchItem{section.FetchItem()}, ch) }()
	var raw []byte
	for msg := range ch {
		r := msg.GetBody(section)
		if r != nil {
			raw, e = io.ReadAll(io.LimitReader(r, mailer.MaxMIME+1))
		}
	}
	if err := <-done; err != nil {
		return m, domain.Content{}, err
	}
	if e != nil || len(raw) == 0 || len(raw) > mailer.MaxMIME {
		return m, domain.Content{}, errors.New("正文不可用或大小超限")
	}
	content, e := mailer.Parse(raw, a.Store.Object)
	if e != nil {
		return m, content, e
	}
	rawID, e := a.Store.Object(raw)
	if e != nil {
		return m, content, e
	}
	b, _ := json.Marshal(content)
	contentID, e := a.Store.Object(b)
	if e != nil {
		return m, content, e
	}
	e = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		v, ok := s.Messages[id]
		if !ok || v.UIDValidity != m.UIDValidity {
			return nil, storage.ErrConflict
		}
		v.RawID = rawID
		v.ContentID = contentID
		v.CacheBytes = int64(len(raw) + len(b))
		for _, at := range content.Attachments {
			v.CacheBytes += at.Size
		}
		if v.CacheBytes > int64(s.Settings.CacheMiB)<<20 {
			return nil, errors.New("此邮件缓存超过当前预算，请提高缓存预算后重试")
		}
		v.CachedAt = time.Now().UTC()
		preview := []rune(strings.Join(strings.Fields(content.Text), " "))
		v.Preview = string(preview[:min(len(preview), 160)])
		v.HasAttachments = len(content.Attachments) > 0
		v.Revision++
		m = v
		ops := []storage.Op{storage.Put("messages", id, v)}
		total := v.CacheBytes
		var cached []domain.Message
		for otherID, other := range s.Messages {
			if otherID != id && other.ContentID != "" {
				total += other.CacheBytes
				cached = append(cached, other)
			}
		}
		slices.SortFunc(cached, func(a, b domain.Message) int { return a.CachedAt.Compare(b.CachedAt) })
		for _, other := range cached {
			if total <= int64(s.Settings.CacheMiB)<<20 {
				break
			}
			total -= other.CacheBytes
			other.RawID = ""
			other.ContentID = ""
			other.CacheBytes = 0
			other.CachedAt = time.Time{}
			other.Revision++
			ops = append(ops, storage.Put("messages", other.ID, other))
		}
		return ops, nil
	})
	if e == nil {
		a.Changed()
	}
	return m, content, e
}
func (a *App) SaveDraft(d domain.Draft, body string) (domain.Draft, error) {
	if len(body) > 2<<20 {
		return d, errors.New("正文超过 2 MiB")
	}
	if len(d.Subject) > 2048 {
		return d, errors.New("主题过长")
	}
	bodyID, e := a.Store.Object([]byte(body))
	if e != nil {
		return d, e
	}
	d.BodyID = bodyID
	isNew := d.ID == ""
	if isNew {
		d.ID = storage.ID()
		d.Attachments = []domain.Attachment{}
	}
	d.UpdatedAt = time.Now().UTC()
	e = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		if _, ok := s.Accounts[d.AccountID]; !ok {
			return nil, errors.New("请选择发件邮箱")
		}
		for _, j := range s.Jobs {
			if j.DraftID == d.ID && j.State != "cancelled" {
				return nil, errors.New("草稿已加入发件箱，取消任务后才可继续编辑")
			}
		}
		if old, ok := s.Drafts[d.ID]; ok {
			if old.Revision != d.Revision {
				return nil, storage.ErrConflict
			}
			d.Attachments = old.Attachments
		} else if !isNew {
			return nil, errors.New("草稿不存在")
		}
		d.Revision++
		return []storage.Op{storage.Put("drafts", d.ID, d)}, nil
	})
	if e == nil {
		a.Changed()
	}
	return d, e
}
func (a *App) Queue(draftID, key string, revision int) (domain.SendJob, error) {
	var result domain.SendJob
	if len(key) < 16 || len(key) > 128 {
		return result, errors.New("缺少有效发送幂等键")
	}
	var existing bool
	a.Store.Read(func(s domain.State, i storage.Index) {
		if id, ok := i.Keys[key]; ok {
			result = s.Jobs[id]
			existing = true
		}
	})
	if existing {
		return result, nil
	}
	var d domain.Draft
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { d = s.Drafts[draftID]; x = s.Accounts[d.AccountID] })
	if d.ID == "" || d.Revision != revision {
		return result, storage.ErrConflict
	}
	body, e := a.Store.LoadObject(d.BodyID)
	if e != nil {
		return result, e
	}
	raw, rcpts, e := mailer.Build(x, d, string(body), a.Store.LoadObject)
	if e != nil {
		return result, e
	}
	rawID, e := a.Store.Object(raw)
	if e != nil {
		return result, e
	}
	result = domain.SendJob{Subject: d.Subject, To: d.To, ID: storage.ID(), DraftID: d.ID, AccountID: x.ID, Key: key, State: "queued", RawID: rawID, Recipients: rcpts, Archive: "pending", CreatedAt: time.Now().UTC()}
	e = a.Store.Update(func(s domain.State, i storage.Index) ([]storage.Op, error) {
		if id, ok := i.Keys[key]; ok {
			result = s.Jobs[id]
			return nil, nil
		}
		if s.Drafts[d.ID].Revision != d.Revision {
			return nil, storage.ErrConflict
		}
		for _, j := range s.Jobs {
			if j.DraftID == d.ID && j.State != "cancelled" {
				return nil, errors.New("该草稿已加入发件箱，请在发件箱查看")
			}
		}
		return []storage.Op{storage.Put("jobs", result.ID, result)}, nil
	})
	if e == nil {
		a.Changed()
		a.Trigger(x.ID)
	}
	return result, e
}
func (a *App) job(j domain.SendJob) error {
	return a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		old, ok := s.Jobs[j.ID]
		if !ok {
			return nil, errors.New("发送任务不存在")
		}
		if (j.State == "sending" && old.State != "queued") || (j.State == "submitting" && old.State != "sending") || old.State == "cancelled" {
			return nil, storage.ErrConflict
		}
		if j.State == "sent" && old.State != "submitting" && old.State != "sent" {
			return nil, storage.ErrConflict
		}
		ops := []storage.Op{storage.Put("jobs", j.ID, j)}
		if j.State == "sent" && (j.Archive == "saved" || j.Archive == "server" || j.Archive == "unknown") {
			sent := sentFolder(s, j.AccountID)
			var folders []domain.SyncFolder
			if sent.ID != "" {
				folders = []domain.SyncFolder{{MailboxID: sent.ID, Name: sent.Name, NewMail: true}}
			}
			ops = append(ops, deltaOps(s, j.AccountID, "send:sent", folders, sent.ID == "", false)...)
		}
		if j.State == "sent" && old.State == "submitting" {
			ops = append(ops, storage.Del("drafts", j.DraftID))
		}
		return ops, nil
	})
}
func (a *App) sendJobs(x domain.Account, c secrets.Credentials) {
	var jobs []domain.SendJob
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, j := range s.Jobs {
			if j.AccountID == x.ID && (j.State == "queued" || j.State == "sent" && j.Archive == "pending") {
				jobs = append(jobs, j)
			}
		}
	})
	for _, j := range jobs {
		if a.ctx.Err() != nil {
			return
		}
		raw, e := a.Store.LoadObject(j.RawID)
		if e != nil {
			if j.State == "sent" {
				j.Archive = "failed"
			} else {
				j.State = "failed"
			}
			j.Error = "发送快照缺失"
			a.job(j)
			continue
		}
		if j.State == "queued" {
			j.State = "sending"
			if a.job(j) != nil {
				return
			}
			unknown, e := mailer.Send(a.ctx, x, c.SMTP, j.Recipients, raw, func() error { j.State = "submitting"; return a.job(j) })
			if e != nil {
				if unknown {
					j.State = "unknown"
				} else {
					j.State = "failed"
				}
				j.Error = cleanError(e)
				if a.job(j) != nil {
					return
				}
				a.Changed()
				continue
			}
			j.State = "sent"
			j.Error = ""
			if a.job(j) != nil {
				return
			}

		}
		if x.AutoArchive {
			j.Archive = "server"
		} else {
			var sent domain.Mailbox
			a.Store.Read(func(s domain.State, _ storage.Index) { sent = sentFolder(s, x.ID) })
			if sent.ID == "" {
				if err := a.discoverSent(x, c.IMAP); err != nil {
					j.Archive = "failed"
					j.Error = "邮件已发送，发现已发送目录失败：" + cleanError(err)
					if a.job(j) != nil {
						return
					}
					a.Changed()
					continue
				}
				a.Store.Read(func(s domain.State, _ storage.Index) { sent = sentFolder(s, x.ID) })
			}
			if sent.ID == "" {
				j.Archive = "failed"
				j.Error = "邮件已发送，未找到已发送文件夹，归档失败"
			} else {
				conn, err := a.dialIMAP(a.ctx, x, c.IMAP)
				if err == nil {
					j.Archive = "submitting"
					if e := a.job(j); e != nil {
						conn.Terminate()
						return
					}
					err = conn.Append(sent.Name, []string{imap.SeenFlag}, time.Now(), strings.NewReader(string(raw)))
					conn.Terminate()
				}
				if err != nil {
					if j.Archive == "submitting" {
						j.Archive = "unknown"
						j.Error = "邮件已发送，归档结果待确认；不会自动重放 APPEND"
					} else {
						j.Archive = "failed"
						j.Error = "邮件已发送，归档连接失败：" + cleanError(err)
					}
				} else {
					j.Archive = "saved"
				}
			}
		}
		if a.job(j) != nil {
			return
		}
		a.Changed()
	}
}
func (a *App) Operate(ids []string, kind, target string, value bool) error {
	if len(ids) == 0 || len(ids) > 200 {
		return errors.New("请选择 1–200 封邮件")
	}
	if !slices.Contains([]string{"seen", "starred", "move", "trash"}, kind) {
		return errors.New("不支持的邮件操作")
	}
	accounts := map[string]bool{}
	e := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for _, id := range ids {
			m, ok := s.Messages[id]
			if !ok {
				return nil, errors.New("邮件不存在，请刷新")
			}
			dest := target
			if kind == "trash" {
				dest = ""
				for _, b := range s.Mailboxes {
					if b.AccountID == m.AccountID && b.Role == "trash" {
						dest = b.ID
					}
				}
				if dest == "" {
					return nil, errors.New("没有已删除文件夹，请使用移动操作；不会直接永久删除")
				}
			}
			if kind == "move" || kind == "trash" {
				b, ok := s.Mailboxes[dest]
				if !ok || b.AccountID != m.AccountID {
					return nil, errors.New("目标文件夹无效")
				}
			}
			for _, o := range s.Operations {
				if o.MessageID == id && o.State == "running" {
					return nil, errors.New("邮件操作正在执行，请稍后重试")
				}
			}
			op := domain.Operation{ID: storage.ID(), AccountID: m.AccountID, MessageID: id, Kind: kind, Target: dest, Value: value, State: "queued", CreatedAt: time.Now().UTC()}
			if kind == "seen" {
				m.Seen = value
			}
			if kind == "starred" {
				m.Starred = value
			}
			m.Revision++
			ops = append(ops, storage.Put("messages", id, m), storage.Put("operations", op.ID, op))
			accounts[m.AccountID] = true
		}
		return ops, nil
	})
	if e == nil {
		a.Changed()
		for id := range accounts {
			a.Trigger(id)
		}
	}
	return e
}
func (a *App) processOperations(x domain.Account, cred secrets.Credentials) {
	var operations []domain.Operation
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, o := range s.Operations {
			if o.AccountID == x.ID && o.State == "queued" {
				operations = append(operations, o)
			}
		}
	})
	slices.SortFunc(operations, func(a, b domain.Operation) int { return a.CreatedAt.Compare(b.CreatedAt) })
	for _, o := range operations {
		var m domain.Message
		var box, target domain.Mailbox
		a.Store.Read(func(s domain.State, _ storage.Index) {
			m = s.Messages[o.MessageID]
			box = s.Mailboxes[m.MailboxID]
			target = s.Mailboxes[o.Target]
		})
		o.State = "running"
		if a.Store.Update(func(_ domain.State, _ storage.Index) ([]storage.Op, error) {
			return []storage.Op{storage.Put("operations", o.ID, o)}, nil
		}) != nil {
			return
		}
		var e error
		removed := false
		if m.ID == "" {
			e = errors.New("邮件已不存在")
		} else {
			c, err := a.dialIMAP(a.ctx, x, cred.IMAP)
			e = err
			if e == nil {
				status, err := c.Select(box.Name, false)
				e = err
				if e == nil && status.UidValidity != m.UIDValidity {
					e = errors.New("UIDVALIDITY 已变化，禁止重放操作")
				}
				if e == nil {
					set := new(imap.SeqSet)
					set.AddNum(m.UID)
					if o.Kind == "seen" || o.Kind == "starred" {
						flag := imap.SeenFlag
						if o.Kind == "starred" {
							flag = imap.FlaggedFlag
						}
						item := imap.AddFlags
						if !o.Value {
							item = imap.RemoveFlags
						}
						e = c.UidStore(set, imap.FormatFlagsOp(imap.FlagsOp(item), true), []interface{}{flag}, nil)
					} else {
						supported, err := c.Support("MOVE")
						if err != nil {
							e = err
						} else if !supported {
							e = errors.New("服务器不支持安全 UID MOVE，未执行复制或全局清除")
						} else {
							targetStatus, statusErr := c.Status(target.Name, []imap.StatusItem{imap.StatusUidNext, imap.StatusUidValidity})
							if statusErr == nil && (targetStatus == nil || targetStatus.UidNext == 0 || targetStatus.UidValidity == 0) {
								targetStatus, statusErr = c.Select(target.Name, true)
								if statusErr == nil {
									var upper uint32
									upper, statusErr = syncUpperUID(c, targetStatus)
									if upper == ^uint32(0) {
										statusErr = errors.New("目标目录 UID 已耗尽")
									}
									targetStatus.UidNext = upper + 1
								}
								if statusErr == nil {
									var source *imap.MailboxStatus
									source, statusErr = c.Select(box.Name, false)
									if statusErr == nil && source.UidValidity != m.UIDValidity {
										statusErr = errors.New("来源 UIDVALIDITY 已变化，未移动")
									}
								}
							}
							if statusErr == nil && (targetStatus == nil || targetStatus.UidNext == 0 || targetStatus.UidValidity == 0) {
								statusErr = errors.New("目标目录缺少有效 UID 基线，未移动")
							}
							if statusErr == nil {
								o.TargetUIDFloor = targetStatus.UidNext
								o.TargetUIDValidity = targetStatus.UidValidity
								statusErr = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
									if s.Operations[o.ID].State != "running" || s.Accounts[x.ID].Revision != x.Revision {
										return nil, storage.ErrConflict
									}
									return []storage.Op{storage.Put("operations", o.ID, o)}, nil
								})
							}
							if statusErr == nil {
								e = c.UidMove(set, target.Name)
							} else {
								e = statusErr
							}
							removed = e == nil
						}
					}
				}
				c.Terminate()
			}
		}
		if e != nil {
			o.State = "failed"
			o.Error = cleanError(e)
		} else {
			o.State = "done"
			o.Error = ""
		}
		a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
			ops := []storage.Op{storage.Put("operations", o.ID, o)}
			if removed {
				ops = append(ops, storage.Del("messages", m.ID))
			}
			if o.TargetUIDFloor > 0 {
				ops = append(ops, deltaOps(s, x.ID, "operation:move", []domain.SyncFolder{{MailboxID: box.ID, Name: box.Name, NewMail: true, Membership: true}, {MailboxID: target.ID, Name: target.Name, NewMail: true, ActivityFrom: o.TargetUIDFloor, ActivityValidity: o.TargetUIDValidity}}, false, false)...)
			}
			return ops, nil
		})
		a.Changed()
	}
}
func (a *App) Check(ctx context.Context, x domain.Account, imapPass, smtpPass string) map[string]string {
	if x.ID != "" {
		_, creds, e := a.account(x.ID)
		if e == nil {
			if imapPass == "" {
				imapPass = creds.IMAP
			}
			if smtpPass == "" {
				smtpPass = creds.SMTP
			}
		}
	}
	out := map[string]string{}
	if c, e := a.dialIMAP(ctx, x, imapPass); e != nil {
		out["imap"] = cleanError(e)
	} else {
		c.Terminate()
		out["imap"] = "连接成功"
	}
	if c, e := mailer.DialSMTP(ctx, x.SMTP, smtpPass); e != nil {
		out["smtp"] = cleanError(e)
	} else {
		c.Close()
		out["smtp"] = "连接成功"
	}
	return out
}
func (a *App) AttachmentAllowed(id string) bool {
	found := false
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, d := range s.Drafts {
			for _, at := range d.Attachments {
				if at.ID == id {
					found = true
				}
			}
		}
	})
	return found
}

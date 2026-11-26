package app

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
)

type IdleStatus struct {
	State     string    `json:"state"`
	Connected int       `json:"connected"`
	Total     int       `json:"total"`
	Limited   []string  `json:"limitedFolders,omitempty"`
	Error     string    `json:"error,omitempty"`
	RetryAt   time.Time `json:"retryAt,omitempty"`
	LastEvent time.Time `json:"lastEventAt,omitempty"`
}
type idleWatcher struct {
	capabilities    mailer.IdleNotice
	accountID, name string
	revision        int
	cancel          context.CancelFunc
	status          IdleStatus
}
type idleDemand struct {
	reasons            []string
	accountID, trigger string
	folder             domain.SyncFolder
	discover, recovery bool
	at                 time.Time
	version            uint64
}

func (a *App) maintainIdle(now time.Time) {
	accounts := map[string]domain.Account{}
	boxes := map[string]domain.Mailbox{}
	if a.Store.Writable() {
		a.Store.Read(func(s domain.State, _ storage.Index) {
			for id, x := range s.Accounts {
				if !x.Paused && s.SyncStatus[id].ErrorKind != "authentication" {
					accounts[id] = x
				}
			}
			for id, b := range s.Mailboxes {
				if _, ok := accounts[b.AccountID]; ok && !b.NoSelect {
					boxes[id] = b
				}
			}
		})
	}
	var deliveries []string
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	for id := range a.idleAccounts {
		if _, ok := accounts[id]; !ok {
			delete(a.idleAccounts, id)
		}
	}
	for key, d := range a.idlePending {
		_, exists := accounts[d.accountID]
		_, folderExists := boxes[d.folder.MailboxID]
		if !exists || d.folder.MailboxID != "" && !folderExists {
			delete(a.idlePending, key)
		}
	}
	for id, x := range accounts {
		if rev, ok := a.idleAccounts[id]; !ok || rev != x.Revision {
			for key, d := range a.idlePending {
				if d.accountID == id {
					delete(a.idlePending, key)
				}
			}
			a.idleAccounts[id] = x.Revision
			delete(a.idleLimits, id)
			deliveries = append(deliveries, id)
			a.demandLocked("discover:"+id, id, domain.SyncFolder{}, "startup", true, true, now)
		}
	}
	ids := make([]string, 0, len(boxes))
	for id := range boxes {
		ids = append(ids, id)
	}
	priority := func(role string) int {
		switch role {
		case "inbox":
			return 0
		case "sent":
			return 1
		case "archive":
			return 2
		}
		return 3
	}
	slices.SortFunc(ids, func(l, r string) int {
		p, q := priority(boxes[l].Role), priority(boxes[r].Role)
		if a.idleFocused[boxes[l].AccountID] == l {
			p = -1
		}
		if a.idleFocused[boxes[r].AccountID] == r {
			q = -1
		}
		if p != q {
			return p - q
		}
		if boxes[l].Name < boxes[r].Name {
			return -1
		}
		return 1
	})
	allowed := map[string]bool{}
	counts := map[string]int{}
	a.idleUnwatched = map[string][]string{}
	a.idleTotals = map[string]int{}
	for _, id := range ids {
		b := boxes[id]
		x := accounts[b.AccountID]
		limit := imapLimit(x) - 3
		if reduced, ok := a.idleLimits[x.ID]; ok {
			limit = min(limit, reduced)
		}
		a.idleTotals[x.ID]++
		if counts[x.ID] < limit {
			allowed[id] = true
			counts[x.ID]++
		} else {
			a.idleUnwatched[x.ID] = append(a.idleUnwatched[x.ID], b.Name)
		}
	}
	for id, w := range a.idleWatchers {
		b, exists := boxes[id]
		if !exists || !allowed[id] || w.revision != accounts[w.accountID].Revision || w.name != b.Name {
			w.cancel()
			delete(a.idleWatchers, id)
		}
	}
	for _, id := range ids {
		if !allowed[id] || a.idleWatchers[id] != nil {
			continue
		}
		b := boxes[id]
		x := accounts[b.AccountID]
		ctx, cancel := context.WithCancel(a.ctx)
		w := &idleWatcher{accountID: x.ID, name: b.Name, revision: x.Revision, cancel: cancel, status: IdleStatus{State: "connecting"}}
		a.idleWatchers[id] = w
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.watchFolder(ctx, id, w, x) }()
	}
	a.mu.Unlock()
	for _, id := range deliveries {
		a.kick(id, true)
	}
	a.flushIdle(now)
}

func (a *App) demandLocked(key, id string, f domain.SyncFolder, reason string, discover, recovery bool, now time.Time) {
	d, exists := a.idlePending[key]
	if !exists {
		d = idleDemand{accountID: id, at: now, trigger: reason, folder: f}
	}
	d.folder.NewMail = d.folder.NewMail || f.NewMail
	d.folder.Flags = d.folder.Flags || f.Flags
	d.folder.Membership = d.folder.Membership || f.Membership
	d.folder.UIDs = append(d.folder.UIDs, f.UIDs...)
	slices.Sort(d.folder.UIDs)
	d.folder.UIDs = slices.Compact(d.folder.UIDs)
	if len(d.folder.UIDs) > 1000 {
		d.folder.UIDs = nil
		d.folder.Flags = true
	}
	d.discover = d.discover || discover
	d.recovery = d.recovery || recovery
	if !slices.Contains(d.reasons, reason) && len(d.reasons) < 32 {
		d.reasons = append(d.reasons, reason)
	}
	d.version++
	a.idlePending[key] = d
}
func (a *App) idleEvent(id string, w *idleWatcher, kind mailer.IdleEvent) {
	a.idleNotice(id, w, mailer.IdleNotice{Kind: kind})
}
func (a *App) idleNotice(id string, w *idleWatcher, n mailer.IdleNotice) {
	a.mu.Lock()
	if a.idleWatchers[id] != w || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	w.status.LastEvent = now
	if n.Kind == mailer.IdleCapabilities {
		w.capabilities = n
	} else if n.Kind == mailer.IdleAlert {
		w.status.Error = "服务器发出 IDLE 状态提示，请检查邮箱服务状态"
	} else {
		f := domain.SyncFolder{MailboxID: id, Name: w.name}
		switch n.Kind {
		case mailer.IdleNewMail:
			f.NewMail = true
		case mailer.IdleFlags:
			if n.UID > 0 {
				f.UIDs = []uint32{n.UID}
			} else {
				f.Flags = true
			}
		default:
			f.NewMail = true
			f.Flags = true
			f.Membership = true
		}
		reason := n.Reason
		if reason == "" {
			reason = "idle:" + string(n.Kind)
		}
		a.demandLocked(id, w.accountID, f, reason, false, false, now)
	}
	a.mu.Unlock()
	a.Changed()
}
func (a *App) watchFolder(ctx context.Context, id string, w *idleWatcher, x domain.Account) {
	failures := 0
	discoveryRequested := false
	for ctx.Err() == nil {
		current, cred, err := a.account(x.ID)
		if err == nil && (current.Revision != w.revision || current.Paused) {
			return
		}
		started := time.Now()
		if err == nil {
			release, e := a.acquireIMAP(ctx, x, true)
			err = e
			if err == nil {
				err = mailer.WatchIMAP(ctx, x.IMAP, cred.IMAP, w.name, func() {
					_ = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
						account, ok := s.Accounts[x.ID]
						st := s.SyncStatus[x.ID]
						if !ok || account.Revision != w.revision || account.Paused || st.ErrorKind != "temporary" || st.RetryAt.IsZero() {
							return nil, nil
						}
						st.RetryAt = time.Time{}
						return []storage.Op{storage.Put("syncStatus", x.ID, st)}, nil
					})
					a.mu.Lock()
					if a.idleWatchers[id] == w {
						w.status.State = "listening"
						w.status.Error = ""
						w.status.RetryAt = time.Time{}
					}
					a.mu.Unlock()
					a.idleNotice(id, w, mailer.IdleNotice{Kind: mailer.IdleReconcile, Reason: "connection-restored"})
				}, func(n mailer.IdleNotice) {
					if ctx.Err() == nil {
						a.idleNotice(id, w, n)
					}
				})
				release()
			}
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= time.Minute {
			failures = 0
		}
		failures++
		state := "reconnecting"
		if errors.Is(err, mailer.ErrIDLEUnsupported) {
			state = "unsupported"
		}
		if syncErrorKind(err) == "authentication" {
			state = "authentication"
		}
		var ce *mailer.ConnectionError
		if errors.As(err, &ce) && ce.ConnectionLimit {
			a.reduceIdle(x.ID)
		}
		if !discoveryRequested {
			var ce *mailer.ConnectionError
			if errors.As(err, &ce) && ce.Stage == "IDLE 选择文件夹" {
				discoveryRequested = true
				a.mu.Lock()
				a.demandLocked("discover:"+x.ID, x.ID, domain.SyncFolder{}, "folder-unavailable", true, false, time.Now().UTC())
				a.mu.Unlock()
			}
		}
		delay := idleRetry(failures)
		a.mu.Lock()
		if a.idleWatchers[id] == w {
			w.status.State = state
			w.status.Error = cleanError(err)
			if state == "reconnecting" {
				w.status.RetryAt = time.Now().UTC().Add(delay)
			}
		}
		a.mu.Unlock()
		a.Changed()
		if state != "reconnecting" {
			<-ctx.Done()
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func idleRetry(n int) time.Duration {
	base := time.Second * time.Duration(1<<min(max(n-1, 0), 8))
	return base + time.Duration(rand.Int64N(int64(base/4)+1))
}
func (a *App) flushIdle(now time.Time) {
	a.mu.Lock()
	pending := map[string]idleDemand{}
	for key, d := range a.idlePending {
		if now.Sub(d.at) >= 500*time.Millisecond {
			copy := d
			copy.folder.UIDs = append([]uint32(nil), d.folder.UIDs...)
			pending[key] = copy
		}
	}
	a.mu.Unlock()
	for key, d := range pending {
		blocked := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			x, ok := s.Accounts[d.accountID]
			st := s.SyncStatus[d.accountID]
			blocked = !ok || x.Paused || st.ErrorKind == "authentication" || now.Before(st.RetryAt)
		})
		if blocked || !a.Store.Writable() {
			continue
		}
		a.mu.Lock()
		current, ok := a.idlePending[key]
		if !ok || current.version != d.version && now.Sub(current.at) < 2*time.Second {
			a.mu.Unlock()
			continue
		}
		d = current
		d.folder.UIDs = append([]uint32(nil), current.folder.UIDs...)
		d.reasons = append([]string(nil), current.reasons...)
		delete(a.idlePending, key)
		a.mu.Unlock()
		var folders []domain.SyncFolder
		if d.folder.MailboxID != "" {
			folders = []domain.SyncFolder{d.folder}
		}
		if err := a.queueDelta(d.accountID, d.trigger, folders, d.discover, d.recovery, d.reasons...); err != nil {
			a.mu.Lock()
			a.demandLocked(key, d.accountID, d.folder, d.trigger, d.discover, d.recovery, d.at)
			a.mu.Unlock()
		}
	}
}
func (a *App) IdleStatuses() map[string]IdleStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]IdleStatus{}
	rank := map[string]int{"listening": 0, "connecting": 1, "reconnecting": 2, "unsupported": 3, "authentication": 4}
	for id, total := range a.idleTotals {
		out[id] = IdleStatus{State: "listening", Total: total, Limited: append([]string(nil), a.idleUnwatched[id]...)}
	}
	for _, w := range a.idleWatchers {
		s := out[w.accountID]
		if s.Total == 0 {
			s.Total++
		}
		if w.status.State == "listening" {
			s.Connected++
		}
		if rank[w.status.State] > rank[s.State] {
			s.State = w.status.State
			s.Error = w.status.Error
		}
		if s.Error == "" {
			s.Error = w.status.Error
		}
		if !w.status.RetryAt.IsZero() && (s.RetryAt.IsZero() || w.status.RetryAt.Before(s.RetryAt)) {
			s.RetryAt = w.status.RetryAt
		}
		if w.status.LastEvent.After(s.LastEvent) {
			s.LastEvent = w.status.LastEvent
		}
		out[w.accountID] = s
	}
	for id, s := range out {
		if len(s.Limited) > 0 && s.State == "listening" {
			s.State = "limited"
			s.Error = "部分目录未实时监听，保留完整对齐更新"
		}
		out[id] = s
	}
	return out
}

// Selection is an ephemeral monitoring preference; it never writes mailbox data.
func (a *App) PreferMailbox(id string) {
	var b domain.Mailbox
	a.Store.Read(func(s domain.State, _ storage.Index) { b = s.Mailboxes[id] })
	if b.ID == "" || b.NoSelect {
		return
	}
	a.mu.Lock()
	a.idleFocused[b.AccountID] = b.ID
	a.mu.Unlock()
}

package transport

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a1zaza/nekoport/internal/app"
	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

type session struct {
	Revision int
	CSRF     string
	Expires  time.Time
}
type throttle struct {
	Count int
	Until time.Time
}
type Server struct {
	authMu     sync.Mutex
	authEvents chan struct{}
	App        *app.App
	Assets     http.Handler
	Origins    []string
	mu         sync.Mutex
	sessions   map[string]session
	attempts   map[string]throttle
	ctx        context.Context
}

func New(a *app.App, assets http.Handler, origins []string) *Server {
	return &Server{authEvents: make(chan struct{}), App: a, Assets: assets, Origins: origins, sessions: map[string]session{}, attempts: map[string]throttle{}, ctx: context.Background()}
}
func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, e error) {
	status := http.StatusBadRequest
	code := "INVALID_REQUEST"
	if errors.Is(e, storage.ErrConflict) {
		status = 409
		code = "CONFLICT"
	}
	JSON(w, status, map[string]any{"code": code, "message": e.Error(), "requestId": storage.ID()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("请求内容格式错误或过大")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("请求包含多余内容")
	}
	return nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]bool{"alive": true}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if !s.App.Store.Writable() {
			JSON(w, 503, map[string]bool{"ready": false})
			return
		}
		JSON(w, 200, map[string]bool{"ready": true})
	})
	mux.HandleFunc("GET /api/v1/session", s.getSession)
	mux.HandleFunc("POST /api/v1/login", s.login)
	mux.HandleFunc("POST /api/v1/logout", s.auth(s.logout))
	mux.HandleFunc("GET /api/v1/bootstrap", s.auth(s.bootstrap))
	mux.HandleFunc("PUT /api/v1/auth/account", s.auth(s.changeAccount))
	mux.HandleFunc("PUT /api/v1/settings", s.auth(s.settings))
	mux.HandleFunc("POST /api/v1/accounts", s.auth(s.saveAccount))
	mux.HandleFunc("PUT /api/v1/accounts/{id}", s.auth(s.saveAccount))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}", s.auth(s.deleteAccount))
	mux.HandleFunc("POST /api/v1/connection-checks", s.auth(s.checkAccount))
	mux.HandleFunc("POST /api/v1/accounts/{id}/sync", s.auth(s.syncAccount))
	mux.HandleFunc("PUT /api/v1/accounts/{id}/sync-settings", s.auth(s.syncSettings))
	mux.HandleFunc("POST /api/v1/accounts/{id}/sync-tasks", s.auth(s.createSyncTask))
	mux.HandleFunc("GET /api/v1/accounts/{id}/sync-tasks", s.auth(s.syncTasks))
	mux.HandleFunc("GET /api/v1/sync-tasks/{id}", s.auth(s.syncTask))
	mux.HandleFunc("POST /api/v1/sync-tasks/{id}/{action}", s.auth(s.syncAction))
	mux.HandleFunc("GET /api/v1/messages", s.auth(s.messages))
	mux.HandleFunc("GET /api/v1/messages/{id}", s.auth(s.message))
	mux.HandleFunc("GET /api/v1/messages/{id}/raw", s.auth(s.rawMessage))
	mux.HandleFunc("POST /api/v1/messages/{id}/forward", s.auth(s.forward))
	mux.HandleFunc("GET /api/v1/messages/{id}/attachments/{attachment}", s.auth(s.messageAttachment))
	mux.HandleFunc("POST /api/v1/message-operations", s.auth(s.operation))
	mux.HandleFunc("POST /api/v1/operations/{id}/retry", s.auth(s.retryOperation))
	mux.HandleFunc("GET /api/v1/drafts/{id}", s.auth(s.draft))
	mux.HandleFunc("POST /api/v1/drafts", s.auth(s.saveDraft))
	mux.HandleFunc("PUT /api/v1/drafts/{id}", s.auth(s.saveDraft))
	mux.HandleFunc("DELETE /api/v1/drafts/{id}", s.auth(s.deleteDraft))
	mux.HandleFunc("POST /api/v1/drafts/{id}/attachments", s.auth(s.upload))
	mux.HandleFunc("DELETE /api/v1/drafts/{id}/attachments/{attachment}", s.auth(s.deleteAttachment))
	mux.HandleFunc("GET /api/v1/drafts/{id}/attachments/{attachment}", s.auth(s.draftAttachment))
	mux.HandleFunc("POST /api/v1/drafts/{id}/send", s.auth(s.send))
	mux.HandleFunc("POST /api/v1/send-jobs/{id}/action", s.auth(s.jobAction))
	mux.HandleFunc("GET /api/v1/events", s.auth(s.events))
	mux.Handle("/", s.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https: http:; frame-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if r.Method != "GET" && r.Method != "HEAD" {
			if !s.originAllowed(r) {
				JSON(w, 403, map[string]string{"message": "请求来源不受信任"})
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host {
		return true
	}
	return slices.Contains(s.Origins, origin)
}
func (s *Server) session(r *http.Request) (session, bool) {
	c, e := r.Cookie("nekoport_session")
	if e != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[c.Value]
	var revision int
	s.App.Store.Read(func(st domain.State, _ storage.Index) { revision = authRevision(st.Settings.AuthRevision) })
	if ok && (time.Now().After(v.Expires) || v.Revision != revision) {
		delete(s.sessions, c.Value)
		return session{}, false
	}
	return v, ok
}
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, ok := s.session(r)
		if !ok {
			JSON(w, 401, map[string]string{"message": "请先登录"})
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(v.CSRF), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			JSON(w, 403, map[string]string{"message": "会话校验失败，请刷新页面"})
			return
		}
		next(w, r)
		if r.Method != "GET" && !s.App.Store.Writable() {
			s.App.Changed()
		}
	}
}
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	v, ok := s.session(r)
	JSON(w, 200, map[string]any{"authenticated": ok, "csrf": v.CSRF})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
		failure(w, errors.New("登录请求格式错误"))
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	t := s.attempts[ip]
	if time.Now().Before(t.Until) && t.Count >= 10 {
		s.mu.Unlock()
		JSON(w, 429, map[string]string{"message": "登录失败次数过多，请 5 分钟后重试"})
		return
	}
	if time.Now().After(t.Until) {
		t = throttle{Until: time.Now().Add(5 * time.Minute)}
	}
	t.Count++
	if len(s.attempts) > 2048 {
		for k, v := range s.attempts {
			if time.Now().After(v.Until) {
				delete(s.attempts, k)
			}
		}
	}
	s.attempts[ip] = t
	s.mu.Unlock()
	var settings domain.Settings
	s.App.Store.Read(func(st domain.State, _ storage.Index) { settings = st.Settings })
	if in.Username != settings.Username || bcrypt.CompareHashAndPassword([]byte(settings.PasswordHash), []byte(in.Password)) != nil {
		JSON(w, 401, map[string]string{"message": "用户名或密码错误"})
		return
	}
	sid := token()
	v := session{CSRF: token(), Expires: time.Now().Add(12 * time.Hour), Revision: authRevision(settings.AuthRevision)}
	s.mu.Lock()
	for k, x := range s.sessions {
		if time.Now().After(x.Expires) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= 4096 {
		s.mu.Unlock()
		JSON(w, 503, map[string]string{"message": "会话过多"})
		return
	}
	s.sessions[sid] = v
	delete(s.attempts, ip)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "nekoport_session", Value: sid, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	JSON(w, 200, map[string]string{"csrf": v.CSRF})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, e := r.Cookie("nekoport_session"); e == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "nekoport_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	JSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	s.App.Store.Read(func(st domain.State, i storage.Index) {
		accounts := []domain.Account{}
		for _, a := range st.Accounts {
			a.CredentialRef = ""
			accounts = append(accounts, a)
		}
		boxes := []map[string]any{}
		for _, b := range st.Mailboxes {
			boxes = append(boxes, map[string]any{"id": b.ID, "accountId": b.AccountID, "name": b.Name, "role": b.Role, "unread": i.Unread[b.ID]})
		}
		drafts := []domain.Draft{}
		for _, d := range st.Drafts {
			drafts = append(drafts, d)
		}
		jobs := []domain.SendJob{}
		for _, j := range st.Jobs {
			j.RawID = ""
			j.Key = ""
			j.Recipients = nil
			j.ToDisplay = mailer.AddressDisplay(j.To)
			if account, ok := st.Accounts[j.AccountID]; ok {
				j.FromDisplay = mailer.MailboxDisplay(account.Name, account.Address)
			}
			jobs = append(jobs, j)
		}
		ops := []domain.Operation{}
		for _, o := range st.Operations {
			if o.State != "done" {
				ops = append(ops, o)
			}
		}
		out["accounts"] = accounts
		out["mailboxes"] = boxes
		out["drafts"] = drafts
		out["jobs"] = jobs
		out["operations"] = ops
		out["settings"] = map[string]any{"fontSize": st.Settings.FontSize, "autoRead": st.Settings.AutoRead, "cacheMiB": st.Settings.CacheMiB, "incrementalMode": "idle"}
		tasks := []domain.SyncTask{}
		for _, t := range st.SyncTasks {
			if t.Active() || t.State == "paused" {
				summary := publicTask(t)
				summary.Folders = nil
				summary.Logs = nil
				tasks = append(tasks, summary)
			}
		}
		out["syncTasks"] = tasks
		out["syncStatus"] = st.SyncStatus
		out["authAccount"] = map[string]any{"username": st.Settings.Username, "revision": authRevision(st.Settings.AuthRevision)}
		out["total"] = len(st.Messages)
	})
	out["storage"] = map[string]bool{"writable": s.App.Store.Writable()}
	out["running"] = s.App.Running()
	out["idleStatus"] = s.App.IdleStatuses()
	JSON(w, 200, out)
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// Accept legacy clients, but incremental delivery is always IDLE-driven.
		QuickSyncIntervalSeconds *int `json:"quickSyncIntervalSeconds"`
		FontSize                 int  `json:"fontSize"`
		AutoRead                 bool `json:"autoRead"`
		CacheMiB                 int  `json:"cacheMiB"`
	}
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	if in.FontSize < 12 || in.FontSize > 24 || in.CacheMiB < 64 || in.CacheMiB > 102400 {
		failure(w, errors.New("设置范围无效"))
		return
	}
	e := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		v := st.Settings
		v.FontSize = in.FontSize
		v.AutoRead = in.AutoRead
		v.CacheMiB = in.CacheMiB
		return []storage.Op{storage.Put("settings", "", v)}, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	s.App.Changed()
	JSON(w, 200, in)
}

type accountInput struct {
	UseSameCredentials bool `json:"useSameCredentials"`
	domain.Account
	IMAPPassword string `json:"imapPassword"`
	SMTPPassword string `json:"smtpPassword"`
}

func (s *Server) sharedCredentials(in *accountInput) error {
	if !in.UseSameCredentials {
		return nil
	}
	in.SMTP.Username = in.IMAP.Username
	if in.IMAPPassword == "" && in.ID != "" {
		var ref string
		s.App.Store.Read(func(st domain.State, _ storage.Index) { ref = st.Accounts[in.ID].CredentialRef })
		creds, e := s.App.Vault.Get(ref)
		if e != nil {
			return errors.New("无法读取已有接收凭据")
		}
		in.IMAPPassword = creds.IMAP
	}
	in.SMTPPassword = in.IMAPPassword
	return nil
}
func (s *Server) saveAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	if r.PathValue("id") != "" {
		in.ID = r.PathValue("id")
	}
	if e := s.sharedCredentials(&in); e != nil {
		failure(w, e)
		return
	}
	v, e := s.App.SaveAccount(in.Account, in.IMAPPassword, in.SMTPPassword)
	if e != nil {
		failure(w, e)
		return
	}
	v.CredentialRef = ""
	JSON(w, 200, v)
}
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if e := s.App.RemoveAccount(r.PathValue("id")); e != nil {
		failure(w, e)
		return
	}
	s.App.Changed()
	JSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) checkAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	if e := s.sharedCredentials(&in); e != nil {
		failure(w, e)
		return
	}
	JSON(w, 200, s.App.Check(r.Context(), in.Account, in.IMAPPassword, in.SMTPPassword))
}
func (s *Server) syncAccount(w http.ResponseWriter, r *http.Request) {
	t, e := s.App.CreateSyncTask(r.PathValue("id"), "quick", "manual", "")
	if e != nil {
		failure(w, e)
		return
	}
	JSON(w, 202, publicTask(t))
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	var msgs []domain.Message
	s.App.Store.Read(func(st domain.State, i storage.Index) {
		for _, id := range i.All {
			m := st.Messages[id]
			if v := q.Get("account"); v != "" && m.AccountID != v {
				continue
			}
			if v := q.Get("mailbox"); v != "" && m.MailboxID != v {
				continue
			}
			if q.Get("view") == "inbox" && st.Mailboxes[m.MailboxID].Role != "inbox" {
				continue
			}
			if q.Get("view") == "starred" && !m.Starred {
				continue
			}
			if q.Get("unread") == "true" && m.Seen {
				continue
			}
			if q.Get("attachments") == "true" && !m.HasAttachments {
				continue
			}
			if d, e := time.Parse("2006-01-02", q.Get("after")); e == nil && m.Date.Before(d) {
				continue
			}
			if d, e := time.Parse("2006-01-02", q.Get("before")); e == nil && !m.Date.Before(d.AddDate(0, 0, 1)) {
				continue
			}
			if cur := q.Get("cursor"); cur != "" {
				b, e := base64.RawURLEncoding.DecodeString(cur)
				var c struct {
					Date time.Time
					ID   string
				}
				if e != nil || json.Unmarshal(b, &c) != nil {
					continue
				}
				if m.Date.After(c.Date) || m.Date.Equal(c.Date) && m.ID >= c.ID {
					continue
				}
			}
			msgs = append(msgs, m)
		}
	})
	out := []domain.Message{}
	needle := strings.ToLower(q.Get("q"))
	partial := false
	next := ""
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	for _, m := range msgs {
		select {
		case <-ctx.Done():
			partial = true
		default:
		}
		if partial {
			break
		}
		next = cursor(m)
		m = mailer.MessageDisplay(m)
		text := m.From + " " + m.To + " " + m.CC + " " + m.Subject + " " + m.Preview + " " + m.FromDisplay + " " + m.ToDisplay + " " + m.CCDisplay
		if needle != "" && !strings.Contains(strings.ToLower(text), needle) {
			if q.Get("body") == "true" && m.ContentID != "" {
				b, e := s.App.Store.LoadObject(m.ContentID)
				if e != nil {
					continue
				}
				var c domain.Content
				if json.Unmarshal(b, &c) != nil || !strings.Contains(strings.ToLower(c.Text), needle) {
					continue
				}
			} else {
				continue
			}
		}
		out = append(out, m)
		if len(out) == limit {
			break
		}
	}
	if len(out) < limit && !partial {
		next = ""
	}
	JSON(w, 200, map[string]any{"items": out, "nextCursor": next, "partial": partial, "scope": "已同步元数据与已缓存正文"})
}
func cursor(m domain.Message) string {
	b, _ := json.Marshal(struct {
		Date time.Time
		ID   string
	}{m.Date, m.ID})
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	m, c, e := s.App.ContentContext(r.Context(), r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	JSON(w, 200, map[string]any{"message": mailer.MessageDisplay(m), "content": c})
}
func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs    []string `json:"ids"`
		Kind   string   `json:"kind"`
		Target string   `json:"target"`
		Value  bool     `json:"value"`
	}
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	if e := s.App.Operate(in.IDs, in.Kind, in.Target, in.Value); e != nil {
		failure(w, e)
		return
	}
	JSON(w, 202, map[string]bool{"queued": true})
}
func (s *Server) retryOperation(w http.ResponseWriter, r *http.Request) {
	var account string
	e := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		o, ok := st.Operations[r.PathValue("id")]
		if !ok || o.State != "failed" {
			return nil, errors.New("只能重试失败操作")
		}
		if o.Kind == "move" || o.Kind == "trash" {
			return nil, errors.New("移动结果可能已生效，请先同步核对并重新选择邮件操作")
		}
		o.State = "queued"
		o.Error = ""
		account = o.AccountID
		return []storage.Op{storage.Put("operations", o.ID, o)}, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	s.App.Trigger(account)
	JSON(w, 200, map[string]bool{"ok": true})
}

type draftInput struct {
	ID        string `json:"id"`
	AccountID string `json:"accountId"`
	To        string `json:"to"`
	CC        string `json:"cc"`
	BCC       string `json:"bcc"`
	Subject   string `json:"subject"`
	InReplyTo string `json:"inReplyTo"`
	Revision  int    `json:"revision"`
	Body      string `json:"body"`
	// Older clients submit the full response object, including updatedAt: "".
	// Accept these read-only fields without parsing or trusting their values.
	BodyID      json.RawMessage `json:"bodyId"`
	Attachments json.RawMessage `json:"attachments"`
	UpdatedAt   json.RawMessage `json:"updatedAt"`
}

func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	var d domain.Draft
	s.App.Store.Read(func(st domain.State, _ storage.Index) { d = st.Drafts[r.PathValue("id")] })
	if d.ID == "" {
		failure(w, errors.New("草稿不存在"))
		return
	}
	body, e := s.App.Store.LoadObject(d.BodyID)
	if e != nil {
		failure(w, e)
		return
	}
	JSON(w, 200, map[string]any{"draft": d, "body": string(body)})
}
func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request) {
	var in draftInput
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	if r.PathValue("id") != "" {
		in.ID = r.PathValue("id")
	}
	d, e := s.App.SaveDraft(domain.Draft{
		ID: in.ID, AccountID: in.AccountID, To: in.To, CC: in.CC, BCC: in.BCC,
		Subject: in.Subject, InReplyTo: in.InReplyTo, Revision: in.Revision,
	}, in.Body)
	if e != nil {
		failure(w, e)
		return
	}
	JSON(w, 200, d)
}
func (s *Server) deleteDraft(w http.ResponseWriter, r *http.Request) {
	e := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		id := r.PathValue("id")
		for _, j := range st.Jobs {
			if j.DraftID == id && j.State != "cancelled" {
				return nil, errors.New("草稿关联发送任务，无法删除")
			}
		}
		return []storage.Op{storage.Del("drafts", id)}, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	s.App.Changed()
	JSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, mailer.MaxAttachment+(1<<20))
	reader, e := r.MultipartReader()
	if e != nil {
		failure(w, e)
		return
	}
	p, e := reader.NextPart()
	if e != nil {
		failure(w, e)
		return
	}
	defer p.Close()
	b, e := io.ReadAll(io.LimitReader(p, mailer.MaxAttachment+1))
	if e != nil || len(b) > mailer.MaxAttachment {
		failure(w, errors.New("单附件不能超过 20 MiB"))
		return
	}
	id, e := s.App.Store.Object(b)
	if e != nil {
		failure(w, e)
		return
	}
	name := p.FileName()
	if name == "" {
		name = "附件"
	}
	at := domain.Attachment{ID: id, Name: name, Type: http.DetectContentType(b), Size: int64(len(b))}
	var result domain.Draft
	e = s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		d, ok := st.Drafts[r.PathValue("id")]
		if !ok {
			return nil, errors.New("草稿不存在")
		}
		for _, j := range st.Jobs {
			if j.DraftID == d.ID && j.State != "cancelled" {
				return nil, errors.New("草稿已加入发件箱，不能更改附件")
			}
		}
		if len(d.Attachments) >= 30 {
			return nil, errors.New("最多 30 个附件")
		}
		d.Attachments = append(append([]domain.Attachment{}, d.Attachments...), at)
		d.Revision++
		d.UpdatedAt = time.Now().UTC()
		result = d
		return []storage.Op{storage.Put("drafts", d.ID, d)}, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	s.App.Changed()
	JSON(w, 200, result)
}
func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	var result domain.Draft
	e := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		d, ok := st.Drafts[r.PathValue("id")]
		if !ok {
			return nil, errors.New("草稿不存在")
		}
		for _, j := range st.Jobs {
			if j.DraftID == d.ID && j.State != "cancelled" {
				return nil, errors.New("草稿已加入发件箱，不能更改附件")
			}
		}
		ats := []domain.Attachment{}
		for _, a := range d.Attachments {
			if a.ID != r.PathValue("attachment") {
				ats = append(ats, a)
			}
		}
		d.Attachments = ats
		d.UpdatedAt = time.Now().UTC()
		d.Revision++
		result = d
		return []storage.Op{storage.Put("drafts", d.ID, d)}, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	JSON(w, 200, result)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, at domain.Attachment) {
	b, e := s.App.Store.LoadObject(at.ID)
	if e != nil {
		failure(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": at.Name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}
func (s *Server) messageAttachment(w http.ResponseWriter, r *http.Request) {
	_, c, e := s.App.ContentContext(r.Context(), r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	for _, at := range c.Attachments {
		if at.ID == r.PathValue("attachment") {
			s.download(w, r, at)
			return
		}
	}
	http.NotFound(w, r)
}
func (s *Server) draftAttachment(w http.ResponseWriter, r *http.Request) {
	var d domain.Draft
	s.App.Store.Read(func(st domain.State, _ storage.Index) { d = st.Drafts[r.PathValue("id")] })
	for _, at := range d.Attachments {
		if at.ID == r.PathValue("attachment") {
			s.download(w, r, at)
			return
		}
	}
	http.NotFound(w, r)
}
func (s *Server) rawMessage(w http.ResponseWriter, r *http.Request) {
	m, _, e := s.App.ContentContext(r.Context(), r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	s.download(w, r, domain.Attachment{ID: m.RawID, Name: "邮件.eml"})
}
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key      string `json:"key"`
		Revision int    `json:"revision"`
	}
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	j, e := s.App.Queue(r.PathValue("id"), in.Key, in.Revision)
	if e != nil {
		failure(w, e)
		return
	}
	j.Key = ""
	j.RawID = ""
	JSON(w, 202, j)
}
func (s *Server) jobAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if e := decode(w, r, &in); e != nil {
		failure(w, e)
		return
	}
	var account string
	e := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		j, ok := st.Jobs[r.PathValue("id")]
		if !ok {
			return nil, errors.New("任务不存在")
		}
		account = j.AccountID
		switch in.Action {
		case "retry":
			if j.State != "failed" {
				return nil, errors.New("只能重试确认失败的任务")
			}
			j.State = "queued"
			j.Error = ""
		case "confirm-sent":
			if j.State != "unknown" {
				return nil, errors.New("任务不是待确认状态")
			}
			j.State = "sent"
			j.Archive = "confirmed"
			j.Error = ""
		case "cancel":
			if j.State != "queued" && j.State != "failed" && j.State != "unknown" {
				return nil, errors.New("任务已提交，不能取消")
			}
			j.State = "cancelled"
		case "archive":
			if j.State != "sent" || j.Archive != "failed" {
				return nil, errors.New("归档结果不确定时请先在远端核对，不自动重放")
			}
			j.Archive = "pending"
		default:
			return nil, errors.New("操作无效")
		}
		ops := []storage.Op{storage.Put("jobs", j.ID, j)}
		if in.Action == "confirm-sent" {
			ops = append(ops, storage.Del("drafts", j.DraftID))
		}
		return ops, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	s.App.Changed()
	s.App.Trigger(account)
	JSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式连接", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	s.mu.Lock()
	authEvents := s.authEvents
	s.mu.Unlock()
	c, unsub := s.App.Subscribe()
	defer unsub()
	fmt.Fprint(w, "event: refresh\ndata: {}\n\n")
	f.Flush()
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-authEvents:
			return
		case <-c:
			if _, ok := s.session(r); !ok {
				return
			}
			fmt.Fprint(w, "event: refresh\ndata: {}\n\n")
			f.Flush()
		case <-t.C:
			if _, ok := s.session(r); !ok {
				return
			}
			fmt.Fprint(w, ": heartbeat\n\n")
			f.Flush()
		}
	}
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	m, c, e := s.App.ContentContext(r.Context(), r.PathValue("id"))
	if e != nil {
		failure(w, e)
		return
	}
	view := mailer.MessageDisplay(m)
	body := "\n\n---------- 转发邮件 ----------\n发件人：" + view.FromDisplay + "\n时间：" + m.Date.Local().Format("2006-01-02 15:04:05 MST") + "\n收件人：" + view.ToDisplay + "\n主题：" + m.Subject + "\n\n" + c.Text
	d, e := s.App.SaveDraft(domain.Draft{AccountID: m.AccountID, Subject: "Fwd: " + m.Subject}, body)
	if e != nil {
		failure(w, e)
		return
	}
	e = s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		v, ok := st.Drafts[d.ID]
		if !ok || v.Revision != d.Revision {
			return nil, storage.ErrConflict
		}
		v.Attachments = append([]domain.Attachment{}, c.Attachments...)
		v.Revision++
		d = v
		return []storage.Op{storage.Put("drafts", d.ID, v)}, nil
	})
	if e != nil {
		failure(w, e)
		return
	}
	s.App.Changed()
	JSON(w, 200, map[string]any{"draft": d, "body": body})
}

package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
)

type protocolCall struct{ command, folder, args string }
type protocolSpy struct {
	failAppend bool
	mu         sync.Mutex
	calls      []protocolCall
	factories  map[string]server.HandlerFactory
}

func (s *protocolSpy) Capabilities(server.Conn) []string { return nil }
func (s *protocolSpy) Command(name string) server.HandlerFactory {
	factory := s.factories[name]
	if factory == nil {
		return nil
	}
	return func() server.Handler { return &spyHandler{Handler: factory(), spy: s, name: name} }
}
func (s *protocolSpy) install(srv *server.Server) {
	s.factories = map[string]server.HandlerFactory{}
	for _, name := range []string{"SELECT", "EXAMINE", "FETCH", "SEARCH", "LIST", "APPEND"} {
		s.factories[name] = srv.Command(name)
	}
	srv.Enable(s)
}
func (s *protocolSpy) reset() { s.mu.Lock(); s.calls = nil; s.mu.Unlock() }
func (s *protocolSpy) snapshot() []protocolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]protocolCall(nil), s.calls...)
}

type spyHandler struct {
	server.Handler
	spy                *protocolSpy
	name, args, target string
}

func (h *spyHandler) Parse(args []interface{}) error {
	h.args = fmt.Sprint(args)
	if len(args) > 0 {
		h.target = fmt.Sprint(args[0])
	}
	return h.Handler.Parse(args)
}
func (h *spyHandler) record(c server.Conn) {
	folder := ""
	if c.Context().Mailbox != nil {
		folder = c.Context().Mailbox.Name()
	}
	if h.name == "SELECT" || h.name == "EXAMINE" || h.name == "APPEND" {
		folder = h.target
	}
	h.spy.mu.Lock()
	h.spy.calls = append(h.spy.calls, protocolCall{h.name, folder, h.args})
	h.spy.mu.Unlock()
}
func (h *spyHandler) Handle(c server.Conn) error {
	h.record(c)
	err := h.Handler.Handle(c)
	if err == nil && h.name == "APPEND" && h.spy.failAppend {
		return errors.New("APPEND completion unavailable")
	}
	return err
}
func (h *spyHandler) UidHandle(c server.Conn) error {
	h.record(c)
	return h.Handler.(server.UidHandler).UidHandle(c)
}
func queuedDelta(t *testing.T, a *App, id string) domain.SyncTask {
	t.Helper()
	var task domain.SyncTask
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, v := range s.SyncTasks {
			if v.AccountID == id && v.Kind == "delta" && v.Active() {
				task = v.Clone()
			}
		}
	})
	if task.ID == "" {
		t.Fatal("missing delta task")
	}
	return task
}
func assertOnlyFolder(t *testing.T, spy *protocolSpy, name string) {
	t.Helper()
	for _, call := range spy.snapshot() {
		if call.command == "LIST" || call.folder != name {
			t.Fatalf("out-of-scope protocol call: %+v", call)
		}
	}
}

func TestDeltaUIDFlagsAndMembershipStayInFolder(t *testing.T) {
	spy := &protocolSpy{}
	a, x, _, user, generation := imapFixtureGeneration(t, spy.install)
	x = configureSyncTest(t, a, x)
	sent, _ := user.GetMailbox("Sent")
	sent.(*memory.Mailbox).Messages = []*memory.Message{{Uid: 6, Date: time.Now(), Size: 49, Body: []byte("From: b@example.test\r\nSubject: keep\r\n\r\nhello")}}
	initial, e := a.CreateSyncTask(x.ID, "reconcile", "manual", "")
	if e != nil {
		t.Fatal(e)
	}
	driveTask(t, a, initial.ID, 60)
	var inbox domain.Mailbox
	var original domain.SyncStatus
	a.Store.Read(func(s domain.State, _ storage.Index) {
		original = s.SyncStatus[x.ID]
		for _, b := range s.Mailboxes {
			if b.Name == "INBOX" {
				inbox = b
			}
		}
	})
	remote, _ := user.GetMailbox("INBOX")
	mb := remote.(*memory.Mailbox)
	mb.Messages[0].Flags = []string{imap.FlaggedFlag}
	spy.reset()
	if e = a.queueDelta(x.ID, "idle:fetch", []domain.SyncFolder{{MailboxID: inbox.ID, UIDs: []uint32{6}}}, false, false); e != nil {
		t.Fatal(e)
	}
	task := driveTask(t, a, queuedDelta(t, a, x.ID).ID, 30)
	if task.ScopeType != "uids" {
		t.Fatal(task.ScopeType)
	}
	assertOnlyFolder(t, spy, "INBOX")
	for _, call := range spy.snapshot() {
		if call.command == "SEARCH" || call.command == "FETCH" && strings.Contains(call.args, "ENVELOPE") {
			t.Fatal("single-UID flag update performed full enumeration", call)
		}
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, m := range s.Messages {
			if m.MailboxID == inbox.ID && (!m.Starred || m.Seen) {
				t.Fatal("flags not updated")
			}
		}
		st := s.SyncStatus[x.ID]
		if !st.LastFull.Equal(original.LastFull) || !st.NextFull.Equal(original.NextFull) || s.Mailboxes[inbox.ID].LastObservedUID != inbox.LastObservedUID {
			t.Fatal("delta changed account schedule or new-mail cursor")
		}
	})
	// Same UID in another folder must survive a double-confirmed source deletion.
	mb.Messages = nil
	spy.reset()
	a.queueDelta(x.ID, "idle:expunge", []domain.SyncFolder{{MailboxID: inbox.ID, Membership: true, NewMail: true}}, false, false)
	driveTask(t, a, queuedDelta(t, a, x.ID).ID, 30)
	assertOnlyFolder(t, spy, "INBOX")
	searches := 0
	for _, call := range spy.snapshot() {
		if call.command == "SEARCH" {
			searches++
		}
	}
	if searches != 2 {
		t.Fatalf("deletion confirmations: %d", searches)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Messages) != 1 {
			t.Fatal("deleted wrong folder", len(s.Messages))
		}
		for _, m := range s.Messages {
			if m.MailboxID == inbox.ID {
				t.Fatal("source UID survived")
			}
		}
	})
	// A generation reset remains scoped to INBOX, leaving Sent's generation alone.
	mb.Messages = []*memory.Message{{Uid: 6, Date: time.Now(), Body: []byte("From: reset@example.test\r\nSubject: reset\r\n\r\nbody")}}
	generation.Store(2)
	spy.reset()
	a.queueDelta(x.ID, "idle:uidvalidity", []domain.SyncFolder{{MailboxID: inbox.ID, NewMail: true, Flags: true, Membership: true}}, false, false)
	driveTask(t, a, queuedDelta(t, a, x.ID).ID, 30)
	assertOnlyFolder(t, spy, "INBOX")
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, m := range s.Messages {
			if m.MailboxID == inbox.ID && m.UIDValidity != 2 || m.MailboxID != inbox.ID && m.UIDValidity != 1 {
				t.Fatal("UID reset escaped folder", m)
			}
		}
	})
}

func TestDeltaImportsOldDateNewUIDAfterRecentBaseline(t *testing.T) {
	spy := &protocolSpy{}
	a, x, _, user, _ := imapFixtureGeneration(t, spy.install)
	x = configureSyncTest(t, a, x)
	first, e := a.CreateSyncTask(x.ID, "quick", "manual", "")
	if e != nil {
		t.Fatal(e)
	}
	driveTask(t, a, first.ID, 50)
	var inbox domain.Mailbox
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, b := range s.Mailboxes {
			if b.Name == "INBOX" {
				inbox = b
			}
		}
	})
	remote, _ := user.GetMailbox("INBOX")
	mb := remote.(*memory.Mailbox)
	mb.Messages = append(mb.Messages, &memory.Message{Uid: 7, Date: time.Now().AddDate(-10, 0, 0), Body: []byte("From: old@example.test\r\nSubject: moved old mail\r\n\r\nhello")})
	spy.reset()
	a.queueDelta(x.ID, "idle:exists", []domain.SyncFolder{{MailboxID: inbox.ID, NewMail: true}}, false, false)
	driveTask(t, a, queuedDelta(t, a, x.ID).ID, 30)
	assertOnlyFolder(t, spy, "INBOX")
	a.Store.Read(func(s domain.State, _ storage.Index) {
		found := false
		for _, m := range s.Messages {
			found = found || m.UID == 7 && m.MailboxID == inbox.ID
		}
		if !found {
			t.Fatal("moved old mail was dropped by date filter")
		}
	})
}
func TestIMAPBudgetReservesOperationSlotsAndCancellation(t *testing.T) {
	a := testApp(t)
	x := domain.Account{ID: "limited", IMAPMaxConnections: 4}
	idle, e := a.acquireIMAP(a.ctx, x, true)
	if e != nil {
		t.Fatal(e)
	}
	defer idle()
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Millisecond)
	defer cancel()
	if _, e = a.acquireIMAP(ctx, x, true); e == nil {
		t.Fatal("idle consumed reserved slot")
	}
	var releases []func()
	for i := 0; i < 3; i++ {
		release, e := a.acquireIMAP(a.ctx, x, false)
		if e != nil {
			t.Fatal(e)
		}
		releases = append(releases, release)
	}
	ctx2, cancel2 := context.WithTimeout(a.ctx, 20*time.Millisecond)
	defer cancel2()
	if _, e = a.acquireIMAP(ctx2, x, false); e == nil {
		t.Fatal("connection budget exceeded")
	}
	for _, release := range releases {
		release()
		release()
	}
	idle()
	idle()
	a.mu.Lock()
	used := a.imapUsage[x.ID].total
	a.mu.Unlock()
	if used != 0 {
		t.Fatal("connection permits leaked", used)
	}
}

func TestDeltaRetryIsLocalAndSurvivesCheckpoint(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x.Paused = false
	next := time.Now().UTC().Add(10 * time.Hour)
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x), storage.Put("syncStatus", x.ID, domain.SyncStatus{AccountID: x.ID, NextFull: next})}, nil
	})
	a.queueDelta(x.ID, "idle:fetch", []domain.SyncFolder{{MailboxID: "first", UIDs: []uint32{42}}}, false, false)
	task := queuedDelta(t, a, x.ID)
	if e := a.failSync(task, -1, errors.New("temporary directory failure")); e != nil {
		t.Fatal(e)
	}
	a.queueDelta(x.ID, "idle:exists", []domain.SyncFolder{{MailboxID: "second", NewMail: true}}, false, false)
	a.Store.Read(func(s domain.State, _ storage.Index) {
		st := s.SyncStatus[x.ID]
		if !st.RetryAt.IsZero() || st.Failures != 0 || !st.NextFull.Equal(next) {
			t.Fatal("one directory blocked account", st)
		}
		failed := s.SyncTasks[task.ID]
		if failed.State != "queued" || failed.RetryAt.IsZero() {
			t.Fatal("missing local retry", failed)
		}
		n := 0
		for _, v := range s.SyncTasks {
			if v.Kind == "delta" && v.RetryAt.IsZero() {
				n++
			}
		}
		if n != 1 {
			t.Fatal("other directory cannot proceed", n)
		}
	})
	root := a.Store.Root()
	if e := a.Store.Checkpoint(); e != nil {
		t.Fatal(e)
	}
	if e := a.Store.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	a.Store = reopened
	a.recoverSyncTasks()
	a.Store.Read(func(s domain.State, _ storage.Index) {
		restored := s.SyncTasks[task.ID]
		if restored.ScopeType != "uids" || restored.Folders[0].UIDs[0] != 42 || restored.RetryAt.IsZero() || restored.Reasons[0] != "idle:fetch" {
			t.Fatal("delta lost during recovery", restored)
		}
		clone := restored.Clone()
		clone.Folders[0].UIDs[0] = 99
		if restored.Folders[0].UIDs[0] != 42 {
			t.Fatal("UID list aliases recovery state")
		}
	})
}

func TestLocalMoveActivityImportsOldMailIntoUninitializedFolder(t *testing.T) {
	a, x, _, user := imapFixture(t)
	x = configureSyncTest(t, a, x)
	sent, _ := user.GetMailbox("Sent")
	mb := sent.(*memory.Mailbox)
	old := time.Now().AddDate(-10, 0, 0)
	mb.Messages = []*memory.Message{
		{Uid: 1, Date: old, Body: []byte("From: old@example.test\r\nSubject: unrelated old mail\r\n\r\nkeep remote only")},
		{Uid: 2, Date: old, Body: []byte("From: old@example.test\r\nSubject: moved old mail\r\n\r\nmoved")},
	}
	box := domain.Mailbox{ID: "new-sent", AccountID: x.ID, Name: "Sent", Role: "sent"}
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("mailboxes", box.ID, box)}, nil
	})
	a.queueDelta(x.ID, "operation:move", []domain.SyncFolder{{MailboxID: box.ID, NewMail: true, ActivityFrom: 2, ActivityValidity: 1}}, false, false)
	driveTask(t, a, queuedDelta(t, a, x.ID).ID, 30)
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Messages) != 1 {
			t.Fatal("forced move changed historical import scope", len(s.Messages))
		}
		for _, m := range s.Messages {
			if m.UID != 2 || m.Subject != "moved old mail" {
				t.Fatal("moved old message missing", m)
			}
		}
	})
}

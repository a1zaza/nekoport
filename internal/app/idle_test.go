package app

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/responses"
	"github.com/emersion/go-imap/server"
)

// A test extension exposes only connections already executing IDLE. Unlike the
// memory backend's polling hook, it allows unsolicited events without any NOOP.
type idleExtension struct {
	enabled bool
	mu      sync.Mutex
	conns   map[string]server.Conn
}

func (e *idleExtension) Capabilities(server.Conn) []string { return nil }
func (e *idleExtension) Command(name string) server.HandlerFactory {
	if !e.enabled || name != "IDLE" {
		return nil
	}
	return func() server.Handler { return &idleHandler{extension: e} }
}

type idleHandler struct {
	server.Idle
	extension *idleExtension
}

func (h *idleHandler) Handle(c server.Conn) error {
	name := c.Context().Mailbox.Name()
	h.extension.mu.Lock()
	h.extension.conns[name] = c
	h.extension.mu.Unlock()
	defer func() {
		h.extension.mu.Lock()
		if h.extension.conns[name] == c {
			delete(h.extension.conns, name)
		}
		h.extension.mu.Unlock()
	}()
	return h.Idle.Handle(c)
}
func (e *idleExtension) connection(t *testing.T, name string) server.Conn {
	t.Helper()
	e.mu.Lock()
	c := e.conns[name]
	e.mu.Unlock()
	if c == nil {
		t.Fatalf("no IDLE connection for %s", name)
	}
	return c
}
func (e *idleExtension) emit(t *testing.T, name string, r imap.WriterTo) {
	t.Helper()
	c := e.connection(t, name)
	select {
	case c.Context().Responses <- r:
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	case <-c.Context().LoggedOut:
		t.Fatal("IDLE connection closed")
	}
}
func waitIdle(t *testing.T, a *App, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		a.maintainIdle(time.Now().UTC())
		a.scheduleSync(time.Now().UTC())
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("IDLE condition timed out; status=%+v", a.IdleStatuses())
}
func idleSettled(a *App, id string) bool {
	busy := false
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, task := range s.SyncTasks {
			busy = busy || task.AccountID == id && task.Active()
		}
	})
	a.mu.Lock()
	_, pending := a.idlePending[id]
	running := a.running[id]
	a.mu.Unlock()
	return !busy && !pending && !running
}

func TestIDLECrossFolderMoveFlagsReconnectAndFolderRename(t *testing.T) {
	ext := &idleExtension{conns: map[string]server.Conn{}}
	a, x, cred, _, _ := imapFixtureGeneration(t, func(s *server.Server) { s.Enable(ext); ext.enabled = true })
	enabled := false
	x.Paused = false
	x.SyncScope = "all"
	x.ReconcileEnabled = &enabled
	if err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, func() bool { s := a.IdleStatuses()[x.ID]; return s.Connected == 3 && idleSettled(a, x.ID) })
	remote, err := mailer.DialIMAP(context.Background(), x.IMAP, cred.IMAP)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Terminate()
	// COPY + source removal simulates a remote MOVE with a different target UID.
	if _, err = remote.Select("INBOX", false); err != nil {
		t.Fatal(err)
	}
	set := new(imap.SeqSet)
	set.AddNum(6)
	if err = remote.UidCopy(set, "Sent"); err != nil {
		t.Fatal(err)
	}
	if err = remote.UidStore(set, imap.AddFlags, []interface{}{imap.DeletedFlag}, nil); err != nil {
		t.Fatal(err)
	}
	if err = remote.Expunge(nil); err != nil {
		t.Fatal(err)
	}
	ext.emit(t, "INBOX", &responses.Expunge{SeqNums: oneSequence(1)})
	status := imap.NewMailboxStatus("Sent", []imap.StatusItem{imap.StatusMessages})
	status.Messages = 1
	ext.emit(t, "Sent", &responses.Select{Mailbox: status})
	var moved domain.Message
	waitIdle(t, a, func() bool {
		ok := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			if len(s.Messages) == 1 {
				for _, m := range s.Messages {
					if s.Mailboxes[m.MailboxID].Name == "Sent" {
						moved = m
						ok = true
					}
				}
			}
		})
		return ok && idleSettled(a, x.ID)
	})
	if moved.UID == 6 {
		t.Fatal("fixture did not change the destination UID")
	}
	if _, err = remote.Select("Sent", false); err != nil {
		t.Fatal(err)
	}
	set = new(imap.SeqSet)
	set.AddNum(moved.UID)
	if err = remote.UidStore(set, imap.StoreItem(imap.SetFlags), []interface{}{imap.SeenFlag, imap.FlaggedFlag}, nil); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *imap.Message, 1)
	ch <- &imap.Message{SeqNum: 1, Uid: moved.UID, Flags: []string{imap.SeenFlag, imap.FlaggedFlag}, Items: map[imap.FetchItem]interface{}{imap.FetchFlags: nil}}
	close(ch)
	ext.emit(t, "Sent", &responses.Fetch{Messages: ch})
	waitIdle(t, a, func() bool {
		ok := false
		a.Store.Read(func(s domain.State, _ storage.Index) { m := s.Messages[moved.ID]; ok = m.Seen && m.Starred })
		return ok && idleSettled(a, x.ID)
	})
	// The return move happens while the source watcher is offline. No events are
	// emitted: reconnect compensation must restore both sides without duplicates.
	ext.connection(t, "Sent").Close()
	if err = remote.UidCopy(set, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if err = remote.UidStore(set, imap.AddFlags, []interface{}{imap.DeletedFlag}, nil); err != nil {
		t.Fatal(err)
	}
	if err = remote.Expunge(nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, func() bool {
		ok := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			if len(s.Messages) == 1 {
				for _, m := range s.Messages {
					ok = s.Mailboxes[m.MailboxID].Name == "INBOX"
				}
			}
		})
		return ok && a.IdleStatuses()[x.ID].Connected == 3 && idleSettled(a, x.ID)
	})
	// A renamed/removed folder is only removed after successful LIST confirmation.
	ext.connection(t, "Sent").Close()
	if err = remote.Rename("Sent", "Archive"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, func() bool {
		old, new := false, false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			for _, b := range s.Mailboxes {
				old = old || b.Name == "Sent"
				new = new || b.Name == "Archive"
			}
		})
		return !old && new && a.IdleStatuses()[x.ID].Connected == 3 && idleSettled(a, x.ID)
	})
	// Local UID MOVE must reconcile both folders without any unsolicited event.
	var source domain.Message
	var archiveID string
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, m := range s.Messages {
			source = m
		}
		for id, b := range s.Mailboxes {
			if b.Name == "Archive" {
				archiveID = id
			}
		}
	})
	if err = a.Operate([]string{source.ID}, "move", archiveID, false); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, func() bool {
		ok := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			if len(s.Messages) == 1 {
				for _, m := range s.Messages {
					ok = m.MailboxID == archiveID
				}
			}
		})
		return ok && idleSettled(a, x.ID)
	})
	ext.connection(t, "Archive").Close()
	if err = remote.Delete("Archive"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, func() bool {
		old := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			for _, b := range s.Mailboxes {
				old = old || b.Name == "Archive"
			}
		})
		return !old && a.IdleStatuses()[x.ID].Connected == 2 && idleSettled(a, x.ID)
	})
	// Pausing an account tears down every long-lived connection.
	x.Paused = true
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	})
	a.maintainIdle(time.Now())
	if len(a.IdleStatuses()) != 0 {
		t.Fatal("paused account still monitored")
	}
}
func oneSequence(n uint32) chan uint32 { ch := make(chan uint32, 1); ch <- n; close(ch); return ch }

func TestIDLEHintsCoalesceAndSurviveActiveTask(t *testing.T) {
	a := testApp(t)
	// Hold both worker slots: all work remains reviewable without opening sockets.
	a.workerGate <- struct{}{}
	a.workerGate <- struct{}{}
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x.Paused = false
	x.SyncScope = "all"
	if err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateSyncTask(x.ID, "history", "manual", ""); err != nil {
		t.Fatal(err)
	}
	w := &idleWatcher{accountID: x.ID}
	a.idleWatchers["box"] = w
	for n := 0; n < 1000; n++ {
		a.idleEvent("box", w, mailer.IdleNewMail)
		a.idleEvent("box", w, mailer.IdleReconcile)
	}
	a.mu.Lock()
	d := a.idlePending[x.ID]
	d.at = time.Now().Add(-time.Second)
	a.idlePending[x.ID] = d
	a.mu.Unlock()
	a.flushIdle(time.Now())
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) != 2 {
			t.Fatal("new mail did not preempt history", len(s.SyncTasks))
		}
	})
	if !a.idlePending[x.ID].full {
		t.Fatal("deletion hint was lost during history")
	}
	a.flushIdle(time.Now())
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) != 2 {
			t.Fatal("event storm queued duplicate work")
		}
	})
	a.idleEvent("box", w, mailer.IdleNewMail)
	a.flushIdle(time.Now().Add(time.Second))
	if !a.idlePending[x.ID].quick {
		t.Fatal("event during quick was lost")
	}
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for id, task := range s.SyncTasks {
			task.State = "succeeded"
			ops = append(ops, storage.Put("syncTasks", id, task))
		}
		return ops, nil
	})
	a.flushIdle(time.Now().Add(time.Second))
	if len(a.idlePending) != 0 {
		t.Fatal("completed tasks did not release pending reconciliation")
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		n := 0
		for _, task := range s.SyncTasks {
			if task.Active() {
				n++
				if task.Kind != "reconcile" || task.Trigger != "idle" {
					t.Fatal(task)
				}
			}
		}
		if n != 1 {
			t.Fatal("missing reconciliation", n)
		}
	})
}

func TestSchedulerNeverPollsIncremental(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x = configureSyncTest(t, a, x)
	disabled := false
	x.ReconcileEnabled = &disabled
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	})
	for n := 0; n < 100; n++ {
		a.scheduleSync(time.Now().Add(time.Duration(n) * time.Hour))
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) != 0 {
			t.Fatal("incremental timer still running")
		}
	})
}

type noIdleExtension struct{ connections atomic.Int32 }

func (*noIdleExtension) Capabilities(server.Conn) []string    { return nil }
func (*noIdleExtension) Command(string) server.HandlerFactory { return nil }
func (e *noIdleExtension) NewConn(c server.Conn) server.Conn {
	e.connections.Add(1)
	return noIdleConn{c}
}

type noIdleConn struct{ server.Conn }

func (c noIdleConn) Capabilities() []string {
	var caps []string
	for _, cap := range c.Conn.Capabilities() {
		if cap != "IDLE" {
			caps = append(caps, cap)
		}
	}
	return caps
}

func TestUnsupportedIDLEDoesNotPollOrReconnect(t *testing.T) {
	ext := &noIdleExtension{}
	a, x, _, _, _ := imapFixtureGeneration(t, func(s *server.Server) { s.Enable(ext) })
	disabled := false
	x.Paused = false
	x.ReconcileEnabled = &disabled
	if err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, a, func() bool {
		s := a.IdleStatuses()[x.ID]
		return s.State == "unsupported" && s.Total == 3 && s.Connected == 0 && idleSettled(a, x.ID)
	})
	before := ext.connections.Load()
	for n := 0; n < 10; n++ {
		a.maintainIdle(time.Now().Add(24 * time.Hour))
		a.scheduleSync(time.Now().Add(24 * time.Hour))
	}
	if ext.connections.Load() != before {
		t.Fatal("unsupported server was polled")
	}
}

func TestIDLEAuthenticationStopsAndConfigurationRestoresConnection(t *testing.T) {
	a, x, _, _, _ := imapFixtureGeneration(t)
	goodRef := x.CredentialRef
	badRef, err := a.Vault.Put(secrets.Credentials{IMAP: "invalid-test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	x.Paused = false
	x.CredentialRef = badRef
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	})
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	w := &idleWatcher{accountID: x.ID, name: "INBOX", revision: x.Revision, cancel: cancel}
	a.idleWatchers["old"] = w
	a.wg.Go(func() { a.watchFolder(ctx, "old", w, x) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := a.IdleStatuses()[x.ID]
		if s.State == "authentication" {
			if !s.RetryAt.IsZero() || strings.Contains(s.Error, "invalid-test-secret") {
				t.Fatal("authentication was retried or leaked a secret", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authentication error was not surfaced", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Updating credentials and revision replaces the parked watcher. A stale
	// network retry deadline must not delay successful reconnect compensation.
	x.CredentialRef = goodRef
	x.Revision++
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x), storage.Put("mailboxes", "inbox", domain.Mailbox{ID: "inbox", AccountID: x.ID, Name: "INBOX", Role: "inbox"}), storage.Put("syncStatus", x.ID, domain.SyncStatus{AccountID: x.ID, Failures: 2, ErrorKind: "temporary", RetryAt: time.Now().Add(time.Hour)})}, nil
	})
	waitIdle(t, a, func() bool {
		ok := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			status := s.SyncStatus[x.ID]
			ok = status.Failures == 0 && status.RetryAt.IsZero() && !status.LastFull.IsZero()
		})
		return ok && a.IdleStatuses()[x.ID].Connected == 3 && idleSettled(a, x.ID)
	})
}

func TestIDLENewMailUsesQuickWithoutPeriodicReconciliation(t *testing.T) {
	ext := &idleExtension{conns: map[string]server.Conn{}}
	a, x, cred, _, _ := imapFixtureGeneration(t, func(s *server.Server) { s.Enable(ext); ext.enabled = true })
	disabled := false
	x.Paused = false
	x.SyncScope = "all"
	x.ReconcileEnabled = &disabled
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	})
	waitIdle(t, a, func() bool { return a.IdleStatuses()[x.ID].Connected == 3 && idleSettled(a, x.ID) })
	var lastFull time.Time
	a.Store.Read(func(s domain.State, _ storage.Index) { lastFull = s.SyncStatus[x.ID].LastFull })
	remote, err := mailer.DialIMAP(context.Background(), x.IMAP, cred.IMAP)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Terminate()
	if err = remote.Append("INBOX", nil, time.Now(), strings.NewReader("From: new@example.test\r\nSubject: IDLE new arrival\r\n\r\nhello")); err != nil {
		t.Fatal(err)
	}
	status := imap.NewMailboxStatus("INBOX", []imap.StatusItem{imap.StatusMessages})
	status.Messages = 2
	ext.emit(t, "INBOX", &responses.Select{Mailbox: status})
	waitIdle(t, a, func() bool {
		ok := false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			status := s.SyncStatus[x.ID]
			ok = len(s.Messages) == 2 && !status.LastQuick.IsZero() && status.LastFull.Equal(lastFull)
		})
		return ok && idleSettled(a, x.ID)
	})
}

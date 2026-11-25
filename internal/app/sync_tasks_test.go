package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
)

func configureSyncTest(t *testing.T, a *App, x domain.Account) domain.Account {
	t.Helper()
	a.cancel() // tests drive steps directly; never start real workers
	x.Paused = false
	if err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	return x
}
func driveTask(t *testing.T, a *App, id string, limit int) domain.SyncTask {
	t.Helper()
	var result domain.SyncTask
	for step := 0; step < limit; step++ {
		a.Store.Read(func(s domain.State, _ storage.Index) { result = s.SyncTasks[id].Clone() })
		if !result.Active() {
			return result
		}
		x, cred, err := a.account(result.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		c, err := mailer.DialIMAP(context.Background(), x.IMAP, cred.IMAP)
		if err != nil {
			t.Fatal(err)
		}
		err = a.syncStep(c, x, result)
		c.Terminate()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatalf("task did not finish: %+v", result)
	return result
}
func TestHistoryImportsOldMailAndResumesWithStableIdentity(t *testing.T) {
	a, x, _, user := imapFixture(t)
	x = configureSyncTest(t, a, x)
	mailbox, _ := user.GetMailbox("INBOX")
	mb := mailbox.(*memory.Mailbox)
	raw := []byte("From: history@example.test\r\nTo: a@example.test\r\nSubject: old history\r\n\r\nold message")
	mb.Messages = nil
	for n := 1; n <= 230; n++ {
		mb.Messages = append(mb.Messages, &memory.Message{Uid: uint32(n * 3), Date: time.Now().AddDate(-15, 0, 0), Size: uint32(len(raw)), Body: raw})
	}
	// Recent-only initial synchronization must not import these fifteen-year-old messages.
	quick, err := a.CreateSyncTask(x.ID, "quick", "manual", "quick-key")
	if err != nil {
		t.Fatal(err)
	}
	if result := driveTask(t, a, quick.ID, 30); result.State != "succeeded" {
		t.Fatal(result)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Messages) != 0 {
			t.Fatal("recent filter ignored")
		}
	})
	task, err := a.CreateSyncTask(x.ID, "history", "manual", "history-key")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := a.CreateSyncTask(x.ID, "history", "manual", "history-key")
	if err != nil || duplicate.ID != task.ID {
		t.Fatal("request not idempotent")
	}
	// Drive to a durable nonempty batch, then cancel and resume.
	for n := 0; n < 12; n++ {
		var value domain.SyncTask
		a.Store.Read(func(s domain.State, _ storage.Index) { value = s.SyncTasks[task.ID].Clone(); x = s.Accounts[x.ID] })
		c, err := mailer.DialIMAP(context.Background(), x.IMAP, "password")
		if err != nil {
			t.Fatal(err)
		}
		err = a.syncStep(c, x, value)
		c.Terminate()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		a.Store.Read(func(s domain.State, _ storage.Index) { count = len(s.Messages) })
		if count > 0 {
			break
		}
	}
	var first domain.Message
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, m := range s.Messages {
			first = m
			break
		}
	})
	if first.ID == "" {
		t.Fatal("no historical progress")
	}
	if err := a.SyncTaskAction(task.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncTaskAction(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	result := driveTask(t, a, task.ID, 50)
	if result.State != "succeeded" {
		t.Fatal(result)
	}
	a.Store.Read(func(s domain.State, i storage.Index) {
		if len(s.Messages) != 230 || len(i.All) != 230 {
			t.Fatal("missing history", len(s.Messages))
		}
		if s.Messages[first.ID].UID != first.UID {
			t.Fatal("identity changed")
		}
		if s.Accounts[x.ID].Scope() != "all" || s.SyncStatus[x.ID].LastFull.IsZero() {
			t.Fatal("scope/status not persisted")
		}
	})
	// Flags are updated on existing metadata and confirmed remote deletion is pruned.
	mb.Messages[0].Flags = []string{imap.SeenFlag}
	mb.Messages = mb.Messages[:229]
	next, err := a.CreateSyncTask(x.ID, "reconcile", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if result = driveTask(t, a, next.ID, 50); result.State != "succeeded" {
		t.Fatal(result)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Messages) != 229 {
			t.Fatal("remote delete not reconciled", len(s.Messages))
		}
		for _, m := range s.Messages {
			if m.UID == 3 && !m.Seen {
				t.Fatal("flags not refreshed")
			}
		}
	})
}
func TestIncompleteScanCannotDeleteAndCancelledBatchCannotCommit(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x = configureSyncTest(t, a, x)
	task, err := a.CreateSyncTask(x.ID, "history", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("messages", "keep", domain.Message{ID: "keep", AccountID: x.ID, MailboxID: "folder", UID: 3, UIDValidity: 9})}, nil
	})
	if err = a.failSync(task, -1, errors.New("network disconnected")); err != nil {
		t.Fatal(err)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if s.Messages["keep"].ID == "" || s.SyncStatus[x.ID].RetryAt.IsZero() {
			t.Fatal("incomplete scan destroyed data")
		}
	})
	if err = a.SyncTaskAction(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) { task = s.SyncTasks[task.ID].Clone() })
	if err = a.SyncTaskAction(task.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err = a.taskCommit(task, task, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Del("messages", "keep")}, nil
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("cancelled batch accepted", err)
	}
}
func TestSchedulerDueAndDisabledAndAuthenticationBackoff(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x = configureSyncTest(t, a, x)
	now := time.Now().UTC()
	// Overdue schedules coalesce to one full task, with at most one auxiliary quick task.
	for n := 0; n < 6; n++ {
		a.scheduleSync(now)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) > 2 || len(s.SyncTasks) < 1 {
			t.Fatal("schedule backlog", len(s.SyncTasks))
		}
	})
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for id, task := range s.SyncTasks {
			task.State = "succeeded"
			ops = append(ops, storage.Put("syncTasks", id, task))
		}
		st := s.SyncStatus[x.ID]
		st.ErrorKind = "authentication"
		ops = append(ops, storage.Put("syncStatus", x.ID, st))
		return ops, nil
	})
	before := 0
	a.Store.Read(func(s domain.State, _ storage.Index) { before = len(s.SyncTasks) })
	a.scheduleSync(now.Add(24 * time.Hour))
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) != before {
			t.Fatal("authentication retried automatically")
		}
	})
	enabled := false
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		v := s.Accounts[x.ID]
		v.ReconcileEnabled = &enabled
		st := s.SyncStatus[x.ID]
		st.ErrorKind = ""
		st.NextQuick = now.Add(time.Hour)
		return []storage.Op{storage.Put("accounts", x.ID, v), storage.Put("syncStatus", x.ID, st)}, nil
	})
	a.scheduleSync(now)
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) != before {
			t.Fatal("disabled schedule ran")
		}
	})
	for n := 1; n < 9; n++ {
		delay := nextRetry(n, now).Sub(now)
		if delay < time.Minute || delay > time.Hour+5*time.Second {
			t.Fatal("backoff out of bounds", delay)
		}
	}
}
func TestSyncTasksPersistAcrossCheckpointRecovery(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x = configureSyncTest(t, a, x)
	task, err := a.CreateSyncTask(x.ID, "history", "manual", "persist-key")
	if err != nil {
		t.Fatal(err)
	}
	task.State = "running"
	task.Stats = domain.SyncCounters{Compared: 102, Added: 100, Deleted: 2}
	appendSyncLog(&task, domain.SyncLog{Action: "compare", Message: "已提交的同步明细", Stats: task.Stats})
	task.Folders = []domain.SyncFolder{{MailboxID: "b", UIDValidity: 7, NextUID: 301, UpperUID: 900, Phase: "scan", Processed: 100}}
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("syncTasks", task.ID, task)}, nil
	})
	root := a.Store.Root()
	if err = a.Store.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err = a.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a.Store = reopened
	a.recoverSyncTasks()
	a.Store.Read(func(s domain.State, _ storage.Index) {
		restored := s.SyncTasks[task.ID]
		if restored.State != "queued" || restored.Folders[0].NextUID != 301 || restored.Stats.Deleted != 2 || len(restored.Logs) != len(task.Logs) {
			t.Fatal("cursor lost", restored)
		}
	})
}

func TestUIDValidityResetConflictsOldOperations(t *testing.T) {
	a, x, _, _, generation := imapFixtureGeneration(t)
	x = configureSyncTest(t, a, x)
	task, err := a.CreateSyncTask(x.ID, "history", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	driveTask(t, a, task.ID, 40)
	var old domain.Message
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, m := range s.Messages {
			old = m
			break
		}
	})
	if err = a.Operate([]string{old.ID}, "starred", "", true); err != nil {
		t.Fatal(err)
	}
	generation.Store(2)
	next, err := a.CreateSyncTask(x.ID, "reconcile", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	result := driveTask(t, a, next.ID, 40)
	if result.State != "succeeded" {
		t.Fatal(result)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if _, ok := s.Messages[old.ID]; ok {
			t.Fatal("stale UID mapping survived")
		}
		for _, m := range s.Messages {
			if m.UIDValidity != 2 {
				t.Fatal("new generation not used")
			}
		}
		for _, op := range s.Operations {
			if op.State != "failed" {
				t.Fatal("old operation could affect reused UID")
			}
		}
	})
}
func TestMissingFolderFailureKeepsDataUntilListConfirmation(t *testing.T) {
	a, x, _, user := imapFixture(t)
	x = configureSyncTest(t, a, x)
	sent, _ := user.GetMailbox("Sent")
	sent.(*memory.Mailbox).Messages = []*memory.Message{{Uid: 1, Date: time.Now(), Size: 44, Body: []byte("From: a@example.test\r\nSubject: sent\r\n\r\nhello")}}
	first, err := a.CreateSyncTask(x.ID, "history", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	driveTask(t, a, first.ID, 40)
	next, err := a.CreateSyncTask(x.ID, "reconcile", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := mailer.DialIMAP(context.Background(), x.IMAP, "password")
	if err != nil {
		t.Fatal(err)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) { next = s.SyncTasks[next.ID].Clone(); x = s.Accounts[x.ID] })
	err = a.syncStep(c, x, next)
	c.Terminate()
	if err != nil {
		t.Fatal(err)
	}
	if err = user.DeleteMailbox("Sent"); err != nil {
		t.Fatal(err)
	}
	result := driveTask(t, a, next.ID, 40)
	if result.State != "partial_failed" {
		t.Fatal(result)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		found := false
		for _, b := range s.Mailboxes {
			if b.Name == "Sent" {
				found = true
			}
		}
		if !found {
			t.Fatal("failed SELECT treated as deletion")
		}
	})
	if err = a.SyncTaskAction(next.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	result = driveTask(t, a, next.ID, 40)
	if result.State != "succeeded" {
		t.Fatal(result)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, b := range s.Mailboxes {
			if b.Name == "Sent" {
				t.Fatal("confirmed missing folder survived")
			}
		}
	})
}

func TestRejectedBatchDoesNotAdvanceRecoveryCursor(t *testing.T) {
	a, x, _, _ := imapFixture(t)
	x = configureSyncTest(t, a, x)
	task, err := a.CreateSyncTask(x.ID, "history", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts[x.ID] })
	c, err := mailer.DialIMAP(context.Background(), x.IMAP, "password")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Terminate()
	for n := 0; n < 15; n++ {
		a.Store.Read(func(s domain.State, _ storage.Index) { task = s.SyncTasks[task.ID].Clone() })
		found := false
		for _, f := range task.Folders {
			if f.Name == "INBOX" && f.Phase == "scan" {
				found = true
			}
		}
		if found {
			break
		}
		if err = a.syncStep(c, x, task); err != nil {
			t.Fatal(err)
		}
	}
	var previous uint64
	for _, f := range task.Folders {
		if f.Name == "INBOX" {
			previous = f.NextUID
		}
	}
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		v := s.Accounts[x.ID]
		v.Revision++
		return []storage.Op{storage.Put("accounts", v.ID, v)}, nil
	})
	if err = a.syncStep(c, x, task); !errors.Is(err, storage.ErrConflict) {
		t.Fatal("expected stale batch rejection", err)
	}
	for _, f := range task.Folders {
		if f.Name == "INBOX" && f.NextUID != previous {
			t.Fatal("failed batch mutated fallback cursor")
		}
	}
}

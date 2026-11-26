package app

import (
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"testing"
	"time"
)

func seedOperationMessage(t *testing.T, a *App) {
	t.Helper()
	if e := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{
			storage.Put("mailboxes", "source", domain.Mailbox{ID: "source", AccountID: "account", Name: "INBOX"}),
			storage.Put("mailboxes", "target", domain.Mailbox{ID: "target", AccountID: "account", Name: "Trash", Role: "trash"}),
			storage.Put("messages", "m", domain.Message{ID: "m", AccountID: "account", MailboxID: "source", UID: 42, UIDValidity: 7, RawID: "saved-mime", ContentPinned: true}),
		}, nil
	}); e != nil {
		t.Fatal(e)
	}
}
func TestPendingMoveKeepsIdentityAndPersistsAcrossRestart(t *testing.T) {
	a := testApp(t)
	seedOperationMessage(t, a)
	if e := a.Operate([]string{"m", "m"}, "trash", "", false); e != nil {
		t.Fatal(e)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		m := s.Messages["m"]
		if len(s.Operations) != 1 || !domain.PendingMoves(s)["m"] || m.MailboxID != "source" || m.UID != 42 || m.UIDValidity != 7 || m.RawID != "saved-mime" || !m.ContentPinned {
			t.Fatal("queue changed canonical identity", m, s.Operations)
		}
	})
	if e := a.Operate([]string{"m"}, "move", "target", false); e == nil {
		t.Fatal("duplicate pending move accepted")
	}
	if e := a.Store.Checkpoint(); e != nil {
		t.Fatal(e)
	}
	root := a.Store.Root()
	if e := a.Store.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	a.Store = reopened
	reopened.Read(func(s domain.State, _ storage.Index) {
		if !domain.PendingMoves(s)["m"] {
			t.Fatal("pending display lost on restart")
		}
	})
	// Simulate an offline submission: the queued move becomes failed, with the
	// original identity still available for the next server reconciliation.
	a.cancel()
	a.processOperations(domain.Account{ID: "account"}, secrets.Credentials{})
	reopened.Read(func(s domain.State, _ storage.Index) {
		if domain.PendingMoves(s)["m"] || s.Messages["m"].UID != 42 {
			t.Fatal("failed move did not restore visibility")
		}
		for _, o := range s.Operations {
			if o.State != "failed" {
				t.Fatal(o)
			}
		}
	})
}
func TestFlagCoalescingAndFailureRollback(t *testing.T) {
	a := testApp(t)
	seedOperationMessage(t, a)
	for _, value := range []bool{true, false, true} {
		if e := a.Operate([]string{"m"}, "seen", "", value); e != nil {
			t.Fatal(e)
		}
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Operations) != 1 || !s.Messages["m"].Seen {
			t.Fatal("queued flags not coalesced")
		}
		for _, o := range s.Operations {
			if o.PreviousValue || !o.Value || !o.Optimistic {
				t.Fatal(o)
			}
		}
	})
	a.cancel()
	a.processOperations(domain.Account{ID: "account"}, secrets.Credentials{})
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if s.Messages["m"].Seen {
			t.Fatal("failed optimistic flag not rolled back")
		}
	})
}
func TestRollbackDoesNotOverwriteNewerIntent(t *testing.T) {
	s := domain.Empty()
	s.Messages["m"] = domain.Message{ID: "m", Seen: true}
	old := domain.Operation{ID: "old", MessageID: "m", Kind: "seen", Optimistic: true, CreatedAt: time.Now()}
	s.Operations["new"] = domain.Operation{ID: "new", MessageID: "m", Kind: "seen", CreatedAt: old.CreatedAt.Add(time.Second)}
	if len(rollbackOperation(s, old)) != 0 {
		t.Fatal("rollback clobbered a newer intent")
	}
}

func TestFailedMoveReconcileOnlyQueuesFolderReads(t *testing.T) {
	a := testApp(t)
	a.cancel()
	seedOperationMessage(t, a)
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		x := s.Accounts["account"]
		x.Paused = false
		return []storage.Op{storage.Put("accounts", x.ID, x), storage.Put("operations", "failed", domain.Operation{ID: "failed", AccountID: x.ID, MessageID: "m", Kind: "trash", State: "failed", Target: "target", TargetUIDFloor: 50, TargetUIDValidity: 9})}, nil
	})
	if e := a.ReconcileOperation("failed"); e != nil {
		t.Fatal(e)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if s.Operations["failed"].State != "failed" || s.Messages["m"].UID != 42 || len(s.SyncTasks) != 2 {
			t.Fatal("reconcile changed operation or mail")
		}
		for _, task := range s.SyncTasks {
			if task.Kind != "delta" || task.Trigger != "operation:manual-check" || len(task.Folders) != 1 {
				t.Fatal(task)
			}
			f := task.Folders[0]
			if !f.NewMail || !f.Membership || !f.Flags {
				t.Fatal(f)
			}
			if f.MailboxID == "target" && (f.ActivityFrom != 50 || f.ActivityValidity != 9) {
				t.Fatal("lost target UID baseline", f)
			}
		}
	})
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		x := s.Accounts["account"]
		x.Paused = true
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	})
	if a.ReconcileOperation("failed") == nil {
		t.Fatal("paused account accepted reconciliation")
	}
}

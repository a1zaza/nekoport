package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
)

func TestSyncLogCountsFollowCommittedComparisonsAndDeletes(t *testing.T) {
	a, x, _, user := imapFixture(t)
	x = configureSyncTest(t, a, x)
	inbox, _ := user.GetMailbox("INBOX")
	sent, _ := user.GetMailbox("Sent")
	message := func(uid uint32) *memory.Message {
		return &memory.Message{Uid: uid, Date: time.Now(), Body: []byte("From: a@example.test\r\nSubject: audit\r\n\r\nbody"), Size: 55}
	}
	mb := inbox.(*memory.Mailbox)
	mb.Messages = []*memory.Message{message(1), message(2)}
	sent.(*memory.Mailbox).Messages = []*memory.Message{message(10)}
	first, err := a.CreateSyncTask(x.ID, "history", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	first = driveTask(t, a, first.ID, 60)
	if first.Stats.Added != 3 || first.Stats.Deleted != 0 || first.Stats.Compared != 3 {
		t.Fatalf("initial counts: %+v", first.Stats)
	}
	if len(first.Logs) == 0 || first.StartedAt.IsZero() {
		t.Fatal("missing persisted detail")
	}
	mb.Messages[0].Flags = []string{imap.SeenFlag}
	mb.Messages = []*memory.Message{mb.Messages[0], message(3)}
	if err = user.DeleteMailbox("Sent"); err != nil {
		t.Fatal(err)
	}
	next, err := a.CreateSyncTask(x.ID, "reconcile", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	next = driveTask(t, a, next.ID, 60)
	if next.Stats != (domain.SyncCounters{Compared: 4, Added: 1, Updated: 1, Deleted: 2}) {
		t.Fatalf("reconcile counts: %+v", next.Stats)
	}
	var sawSnapshot, sawRemoved, sawConfirmed bool
	for _, event := range next.Logs {
		if event.Action == "snapshot" && event.Folder == "INBOX" {
			sawSnapshot = true
		}
		if event.Action == "folder-removed" && event.Stats.Deleted == 1 {
			sawRemoved = true
		}
		if event.Action == "delete-confirm" && event.Stats.Deleted == 1 {
			sawConfirmed = true
		}
	}
	if !sawSnapshot || !sawRemoved || !sawConfirmed {
		t.Fatalf("missing reasons: %+v", next.Logs)
	}
	for _, f := range next.Folders {
		if f.Name == "INBOX" && (f.LocalBefore != 2 || f.RemoteTotal != 2 || f.Stats.Deleted != 1) {
			t.Fatalf("bad snapshot: %+v", f)
		}
	}
	// Quick adds new remote identities but must never claim it reconciled deletion.
	mb.Messages = []*memory.Message{mb.Messages[1], message(4)}
	quick, err := a.CreateSyncTask(x.ID, "quick", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	quick = driveTask(t, a, quick.ID, 60)
	if quick.Stats.Compared != 1 || quick.Stats.Added != 1 || quick.Stats.Deleted != 0 {
		t.Fatal(quick.Stats)
	}
}

func TestSyncRetentionKeepsFivePerCategoryAndRecoveryState(t *testing.T) {
	a := testApp(t)
	a.cancel()
	now := time.Now().UTC()
	err := a.Store.Update(func(_ domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for _, account := range []string{"account", "other"} {
			for _, kind := range []string{"quick", "reconcile", "history"} {
				for n := 0; n < 8; n++ {
					id := fmt.Sprintf("%s-%s-%d", account, kind, n)
					task := domain.SyncTask{ID: id, AccountID: account, Kind: kind, State: "succeeded", UpdatedAt: now.Add(time.Duration(n) * time.Minute)}
					ops = append(ops, storage.Put("syncTasks", id, task))
				}
			}
			for _, state := range []string{"running", "paused"} {
				id := account + state
				ops = append(ops, storage.Put("syncTasks", id, domain.SyncTask{ID: id, AccountID: account, Kind: "history", State: state, UpdatedAt: now.Add(-time.Hour)}))
			}
		}
		return ops, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a.recoverSyncTasks()
	a.Store.Read(func(s domain.State, _ storage.Index) {
		counts := map[string]int{}
		for _, task := range s.SyncTasks {
			if task.State == "paused" || task.Active() {
				continue
			}
			group := "full"
			if task.Kind == "quick" {
				group = "quick"
			}
			counts[task.AccountID+"/"+group]++
		}
		for _, account := range []string{"account", "other"} {
			if counts[account+"/quick"] != 5 || counts[account+"/full"] != 5 {
				t.Fatal(counts)
			}
			if s.SyncTasks[account+"running"].State != "queued" || s.SyncTasks[account+"paused"].State != "paused" {
				t.Fatal("recovery state evicted")
			}
		}
	})
}

func TestSyncLogsCloneBoundsAndRejectedCommit(t *testing.T) {
	task := domain.SyncTask{LogVersion: 1, Stats: domain.SyncCounters{Added: 600}}
	for n := 0; n < syncLogLimit+5; n++ {
		appendSyncLog(&task, domain.SyncLog{Message: fmt.Sprint(n)})
	}
	clone := task.Clone()
	clone.Logs[0].Message = "changed"
	if task.Logs[0].Message != "5" || task.LogsDropped != 5 || len(task.Logs) != syncLogLimit || task.Stats.Added != 600 {
		t.Fatal("log bounds or isolation broken")
	}
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x = configureSyncTest(t, a, x)
	current, err := a.CreateSyncTask(x.ID, "quick", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.SyncTaskAction(current.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err = a.taskCommit(current, current, func(_ domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("messages", "late", domain.Message{ID: "late", MailboxID: "inbox", UID: 1})}, nil
	}); err == nil {
		t.Fatal("late commit accepted")
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		persisted := s.SyncTasks[current.ID]
		if persisted.Stats.Added != 0 || len(persisted.Logs) != 2 || s.Messages["late"].ID != "" {
			t.Fatal("rejected write advanced log")
		}
	})
}

func TestSyncRetentionPrunesOnCompletion(t *testing.T) {
	a := testApp(t)
	a.cancel()
	var x domain.Account
	a.Store.Read(func(s domain.State, _ storage.Index) { x = s.Accounts["account"] })
	x = configureSyncTest(t, a, x)
	current, err := a.CreateSyncTask(x.ID, "quick", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	err = a.Store.Update(func(_ domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for n := 0; n < 5; n++ {
			task := domain.SyncTask{ID: fmt.Sprint(n), AccountID: x.ID, Kind: "quick", State: "succeeded", UpdatedAt: time.Now().Add(-time.Duration(n+1) * time.Hour)}
			ops = append(ops, storage.Put("syncTasks", task.ID, task))
		}
		return ops, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := current.Clone()
	after.State = "succeeded"
	if err = a.taskCommit(current, after, nil); err != nil {
		t.Fatal(err)
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.SyncTasks) != 5 || s.SyncTasks[current.ID].ID == "" || s.SyncTasks["4"].ID != "" {
			t.Fatal("completion retention", len(s.SyncTasks))
		}
	})
}

func TestSyncLogCountsUnimportedRecentScopeMessages(t *testing.T) {
	a, x, _, user := imapFixture(t)
	x = configureSyncTest(t, a, x)
	mailbox, _ := user.GetMailbox("INBOX")
	mailbox.(*memory.Mailbox).Messages = []*memory.Message{{Uid: 1, Date: time.Now().AddDate(-10, 0, 0), Body: []byte("From: old@example.test\r\n\r\nold"), Size: 30}}
	task, err := a.CreateSyncTask(x.ID, "reconcile", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	result := driveTask(t, a, task.ID, 40)
	if result.Stats.Compared != 1 || result.Stats.Skipped != 1 || result.Stats.Added != 0 {
		t.Fatal(result.Stats)
	}
}

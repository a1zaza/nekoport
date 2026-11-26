package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap/backend/memory"
)

func bodyCalls(spy *protocolSpy) int {
	n := 0
	for _, call := range spy.snapshot() {
		if call.command == "FETCH" && strings.Contains(call.args, "BODY.PEEK[") {
			n++
		}
	}
	return n
}
func contentFixture(t *testing.T) (*App, domain.Account, *memory.User, *protocolSpy) {
	spy := &protocolSpy{}
	a, x, _, user, _ := imapFixtureGeneration(t, spy.install)
	x = configureSyncTest(t, a, x)
	sent, _ := user.GetMailbox("Sent")
	raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nSubject: old attachment\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=fixture\r\n\r\n--fixture\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nold body\r\n--fixture\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=test.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nYXR0YWNobWVudA==\r\n--fixture--\r\n")
	sent.(*memory.Mailbox).Messages = []*memory.Message{{Uid: 9, Date: time.Now().AddDate(-10, 0, 0), Size: uint32(len(raw)), Body: raw}}
	return a, x, user, spy
}
func startFull(t *testing.T, a *App, id, trigger string) domain.SyncTask {
	t.Helper()
	task, e := a.CreateSyncTask(id, "reconcile", trigger, "")
	if e != nil {
		t.Fatal(e)
	}
	return task
}

func TestFirstFullSkipsBodiesLaterFullDownloadsAllAndReuses(t *testing.T) {
	a, x, user, spy := contentFixture(t)
	first := driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 60)
	if !first.InitialFull || first.DownloadContent || bodyCalls(spy) != 0 {
		t.Fatal("first full downloaded content", first, bodyCalls(spy))
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Messages) != 1 || !s.SyncStatus[x.ID].FirstFullCompleted {
			t.Fatal("initial recent baseline mismatch")
		}
	})
	spy.reset()
	second := driveTask(t, a, startFull(t, a, x.ID, "timer").ID, 90)
	if second.State != "succeeded" || second.InitialFull || !second.DownloadContent || second.Scope != "all" || second.Content.Downloaded != 2 || bodyCalls(spy) != 2 {
		t.Fatal("subsequent full did not download all", second, spy.snapshot())
	}
	var attachment string
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if len(s.Messages) != 2 || s.Accounts[x.ID].Scope() != "recent" {
			t.Fatal("all-message content scope or account preference changed")
		}
		for _, m := range s.Messages {
			if !m.ContentPinned || !a.contentComplete(m) {
				t.Fatal("incomplete retained content", m)
			}
			if m.Subject == "old attachment" {
				b, e := a.Store.LoadObject(m.ContentID)
				if e != nil {
					t.Fatal(e)
				}
				var c domain.Content
				if json.Unmarshal(b, &c) != nil || len(c.Attachments) != 1 || !strings.Contains(c.Text, "old body") {
					t.Fatal(c)
				}
				attachment = c.Attachments[0].ID
				data, e := a.Store.LoadObject(attachment)
				if e != nil || string(data) != "attachment" {
					t.Fatal("attachment missing", e)
				}
			}
		}
	})
	sent, _ := user.GetMailbox("Sent")
	if len(sent.(*memory.Mailbox).Messages[0].Flags) != 0 {
		t.Fatal("body download changed unread state")
	}
	spy.reset()
	third := driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 90)
	if third.Content.Downloaded != 0 || third.Content.Reused != 2 || bodyCalls(spy) != 0 {
		t.Fatal("complete content downloaded twice", third.Content, spy.snapshot())
	}
	// A missing attachment must not be mistaken for a complete cached message.
	if e := os.Remove(filepath.Join(a.Store.Root(), "objects", attachment[:2], attachment)); e != nil {
		t.Fatal(e)
	}
	spy.reset()
	fourth := driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 90)
	if fourth.Content.Downloaded != 1 || fourth.Content.Reused != 1 || bodyCalls(spy) != 1 {
		t.Fatal("missing attachment was not repaired", fourth.Content)
	}
}

func TestContentDownloadResumeAndCheckpoint(t *testing.T) {
	a, x, _, spy := contentFixture(t)
	driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 60)
	task := startFull(t, a, x.ID, "manual")
	for step := 0; step < 60; step++ {
		a.Store.Read(func(s domain.State, _ storage.Index) { task = s.SyncTasks[task.ID].Clone() })
		if task.Content.Downloaded == 1 {
			break
		}
		c, e := a.dialIMAP(context.Background(), x, "password")
		if e != nil {
			t.Fatal(e)
		}
		e = a.syncStep(c, x, task)
		c.Terminate()
		if e != nil {
			t.Fatal(e)
		}
	}
	if task.Content.Downloaded != 1 || !task.MetadataComplete {
		t.Fatal("no durable body progress", task)
	}
	root := a.Store.Root()
	if e := a.Store.Checkpoint(); e != nil {
		t.Fatal(e)
	}
	a.Store.Close()
	reopened, e := storage.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	a.Store = reopened
	a.recoverSyncTasks()
	if e = a.SyncTaskAction(task.ID, "cancel"); e != nil {
		t.Fatal(e)
	}
	if e = a.SyncTaskAction(task.ID, "resume"); e != nil {
		t.Fatal(e)
	}
	result := driveTask(t, a, task.ID, 90)
	if result.State != "succeeded" || result.Content.Downloaded != 1 || result.Content.Reused != 1 || bodyCalls(spy) != 2 {
		t.Fatal("resume repeated completed downloads", result.Content, bodyCalls(spy))
	}
}

func TestContentLimitReportsFailureAndContinuesOtherMail(t *testing.T) {
	a, x, user, _ := contentFixture(t)
	inbox, _ := user.GetMailbox("INBOX")
	inbox.(*memory.Mailbox).Messages[0].Size = mailer.MaxMIME + 1
	driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 60)
	task := driveTask(t, a, startFull(t, a, x.ID, "timer").ID, 90)
	if task.State != "partial_failed" || task.Content.Failed != 1 || task.Content.Downloaded != 1 {
		t.Fatal("limit silently succeeded or blocked other mail", task)
	}
	// After server-side deletion, retry must re-align metadata and keep the already saved copy.
	inbox.(*memory.Mailbox).Messages = nil
	if e := a.SyncTaskAction(task.ID, "resume"); e != nil {
		t.Fatal(e)
	}
	result := driveTask(t, a, task.ID, 90)
	if result.State != "succeeded" || result.Content.Downloaded != 0 || result.Content.Reused != 1 {
		t.Fatal("retry did not reuse completed content", result)
	}
}

func TestOrdinaryCacheEvictionKeepsFullDownloadedContent(t *testing.T) {
	a, x, user, _ := contentFixture(t)
	driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 60)
	driveTask(t, a, startFull(t, a, x.ID, "manual").ID, 90)
	inboxRemote, _ := user.GetMailbox("INBOX")
	mb := inboxRemote.(*memory.Mailbox)
	for _, uid := range []uint32{7, 8} {
		raw := []byte("From: a@example.test\r\nSubject: ordinary cache\r\n\r\n" + strings.Repeat("a", 300000))
		mb.Messages = append(mb.Messages, &memory.Message{Uid: uid, Date: time.Now(), Size: uint32(len(raw)), Body: raw})
	}
	var inbox domain.Mailbox
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, b := range s.Mailboxes {
			if b.Name == "INBOX" {
				inbox = b
			}
		}
	})
	a.queueDelta(x.ID, "idle:exists", []domain.SyncFolder{{MailboxID: inbox.ID, NewMail: true}}, false, false)
	driveTask(t, a, queuedDelta(t, a, x.ID).ID, 40)
	a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		settings := s.Settings
		settings.CacheMiB = 1
		return []storage.Op{storage.Put("settings", "", settings)}, nil
	})
	ids := map[uint32]string{}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for id, m := range s.Messages {
			if m.MailboxID == inbox.ID {
				ids[m.UID] = id
			}
		}
	})
	for _, uid := range []uint32{7, 8} {
		if _, _, e := a.ContentContext(context.Background(), ids[uid]); e != nil {
			t.Fatal(e)
		}
	}
	a.Store.Read(func(s domain.State, _ storage.Index) {
		if s.Messages[ids[7]].ContentID != "" || s.Messages[ids[8]].ContentID == "" {
			t.Fatal("normal cache budget did not evict old ordinary content")
		}
		pinned := 0
		for _, m := range s.Messages {
			if m.ContentPinned {
				pinned++
				if !a.contentComplete(m) {
					t.Fatal("retained content was evicted")
				}
			}
		}
		if pinned != 2 {
			t.Fatal("lost retained messages", pinned)
		}
	})
}

func TestFullContentPolicyMigrationAndIncrementalIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, kind, trigger string
		status              domain.SyncStatus
		initial, download   bool
	}{
		{"new account", "reconcile", "manual", domain.SyncStatus{}, true, false},
		{"legacy account", "reconcile", "timer", domain.SyncStatus{LastFull: time.Now()}, false, true},
		{"completed first", "history", "manual", domain.SyncStatus{FirstFullCompleted: true}, false, true},
		{"legacy idle full", "reconcile", "idle:fetch", domain.SyncStatus{FirstFullCompleted: true}, false, false},
		{"quick", "quick", "manual", domain.SyncStatus{FirstFullCompleted: true}, false, false},
		{"delta", "delta", "idle:exists", domain.SyncStatus{FirstFullCompleted: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := domain.SyncTask{Kind: tc.kind, Trigger: tc.trigger, Scope: "recent", RecentDays: 30}
			configureFullContent(&task, tc.status)
			if task.InitialFull != tc.initial || task.DownloadContent != tc.download {
				t.Fatal(task)
			}
			if tc.download && (task.Scope != "all" || task.RecentDays != 0) {
				t.Fatal(task)
			}
			configureFullContent(&task, domain.SyncStatus{FirstFullCompleted: true})
			if task.InitialFull != tc.initial || task.DownloadContent != tc.download {
				t.Fatal("persisted task policy changed", task)
			}
		})
	}
}

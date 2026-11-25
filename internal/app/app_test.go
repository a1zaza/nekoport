package app

import (
	"fmt"
	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"path/filepath"
	"strings"
	"testing"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	s, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	key := filepath.Join(dir, "master.key")
	if e = secrets.Generate(key); e != nil {
		t.Fatal(e)
	}
	v, e := secrets.Open(filepath.Join(dir, "secrets.json"), key)
	if e != nil {
		t.Fatal(e)
	}
	a := New(s, v)
	t.Cleanup(func() { a.Close(); s.Close() })
	ref, e := v.Put(secrets.Credentials{IMAP: "test", SMTP: "test"})
	if e != nil {
		t.Fatal(e)
	}
	e = s.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", "account", domain.Account{ID: "account", Name: "测试", Address: "from@example.test", CredentialRef: ref, Paused: true, SyncDays: 30})}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func TestDraftRevisionAndSendIdempotency(t *testing.T) {
	a := testApp(t)
	d, e := a.SaveDraft(domain.Draft{AccountID: "account", To: "to@example.test", Subject: "中文邮件"}, "正文")
	if e != nil {
		t.Fatal(e)
	}
	stale := d
	d, e = a.SaveDraft(d, "新正文")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.SaveDraft(stale, "不该覆盖"); e != storage.ErrConflict {
		t.Fatal("stale draft accepted", e)
	}
	j, e := a.Queue(d.ID, "unique-key-123456789", d.Revision)
	if e != nil {
		t.Fatal(e)
	}
	again, e := a.Queue(d.ID, "unique-key-123456789", d.Revision)
	if e != nil || again.ID != j.ID {
		t.Fatal("duplicate send job", again, e)
	}
	if _, e = a.SaveDraft(d, "发送快照不能修改"); e == nil {
		t.Fatal("frozen draft editable")
	}
	if e = a.Store.Checkpoint(); e != nil {
		t.Fatal(e)
	}
	a.Store.Read(func(st domain.State, i storage.Index) {
		if len(st.Jobs) != 1 || i.Keys[j.Key] != j.ID {
			t.Fatal("idempotency index mismatch")
		}
	})
}
func TestInterruptedSendAndArchiveNeverAutomaticallyReplay(t *testing.T) {
	a := testApp(t)
	e := a.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("jobs", "send", domain.SendJob{ID: "send", AccountID: "account", State: "submitting", Key: "send-key"}), storage.Put("jobs", "archive", domain.SendJob{ID: "archive", AccountID: "account", State: "sent", Archive: "submitting", Key: "archive-key"})}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	a.Start()
	a.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Jobs["send"].State != "unknown" || st.Jobs["archive"].Archive != "unknown" {
			t.Fatal("uncertain effects were retried")
		}
	})
}
func TestCancelledJobCannotBeClaimedByStaleWorker(t *testing.T) {
	a := testApp(t)
	j := domain.SendJob{ID: "job", AccountID: "account", State: "cancelled", Key: "key"}
	a.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("jobs", j.ID, j)}, nil
	})
	j.State = "sending"
	if a.job(j) == nil {
		t.Fatal("cancelled SMTP job reclaimed")
	}
}
func TestUnsafeDeleteAndCrossAccountMoveRejected(t *testing.T) {
	a := testApp(t)
	a.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("messages", "message", domain.Message{ID: "message", AccountID: "account", MailboxID: "inbox", UID: 1}), storage.Put("mailboxes", "other", domain.Mailbox{ID: "other", AccountID: "other-account"})}, nil
	})
	if a.Operate([]string{"message"}, "trash", "", false) == nil {
		t.Fatal("deleted without trash folder")
	}
	if a.Operate([]string{"message"}, "move", "other", false) == nil {
		t.Fatal("cross-account move accepted")
	}
}

func TestCachedHTMLStyleUpgrade(t *testing.T) {
	for _, version := range []int{0, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			a := testApp(t)
			raw := []byte("From: a@example.test\r\nTo: b@example.test\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body bgcolor=\"#fdf6e3\" style=\"background-color:#fdf6e3\"><style>.card{padding:24px;display:flex;gap:12px}</style><p class=\"card\" style=\"font-size:18px\">正文</p>")
			rawID, err := a.Store.Object(raw)
			if err != nil {
				t.Fatal(err)
			}
			old := []byte(fmt.Sprintf(`{"htmlVersion":%d,"text":"正文","html":"<p>正文</p>","attachments":[]}`, version))
			contentID, err := a.Store.Object(old)
			if err != nil {
				t.Fatal(err)
			}
			err = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				return []storage.Op{storage.Put("messages", "cached", domain.Message{ID: "cached", AccountID: "account", RawID: rawID, ContentID: contentID, CacheBytes: int64(len(raw) + len(old))})}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			msg, content, err := a.Content("cached")
			if err != nil {
				t.Fatal(err)
			}
			if content.HTMLVersion != mailer.HTMLVersion || !strings.Contains(content.HTML, "gap") || !strings.Contains(content.HTML, "padding") || !strings.Contains(content.HTML, "font-size") || !strings.Contains(content.HTML, `bgcolor="#fdf6e3"`) {
				t.Fatalf("styles not restored: %+v", content)
			}
			if msg.ContentID == contentID {
				t.Fatal("upgrade not persisted")
			}
			again, _, err := a.Content("cached")
			if err != nil || again.ContentID != msg.ContentID || again.Revision != msg.Revision {
				t.Fatal("upgrade repeated", err)
			}
		})
	}
}

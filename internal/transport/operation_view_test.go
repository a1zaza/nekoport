package transport

import (
	"encoding/json"
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"testing"
)

func TestQueuedMoveImmediatelyUpdatesListsAndUnread(t *testing.T) {
	s, h := testServer(t)
	s.App.Close()
	cookie, csrf := loginTest(t, h)
	if e := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{
			storage.Put("mailboxes", "inbox", domain.Mailbox{ID: "inbox", AccountID: "test-account", Name: "INBOX", Role: "inbox"}),
			storage.Put("mailboxes", "trash", domain.Mailbox{ID: "trash", AccountID: "test-account", Name: "Trash", Role: "trash"}),
			storage.Put("messages", "m", domain.Message{ID: "m", AccountID: "test-account", MailboxID: "inbox", UID: 9, UIDValidity: 17}),
		}, nil
	}); e != nil {
		t.Fatal(e)
	}
	w := req(h, "POST", "/api/v1/message-operations", "http://localhost:8080", map[string]any{"ids": []string{"m"}, "kind": "trash"}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, query := range []string{"?mailbox=inbox", "?view=inbox", "?unread=true", "?q=test"} {
		w = req(h, "GET", "/api/v1/messages"+query, "", nil, cookie, "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var page struct {
			Items []domain.Message `json:"items"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 {
			t.Fatal(query, w.Body.String())
		}
	}
	w = req(h, "GET", "/api/v1/bootstrap", "", nil, cookie, "")
	var boot struct {
		Total     int `json:"total"`
		Mailboxes []struct {
			ID     string `json:"id"`
			Unread int    `json:"unread"`
		} `json:"mailboxes"`
	}
	if json.Unmarshal(w.Body.Bytes(), &boot) != nil || boot.Total != 0 {
		t.Fatal(w.Body.String())
	}
	for _, b := range boot.Mailboxes {
		if b.Unread != 0 {
			t.Fatal("pending unread still counted", b)
		}
	}
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for _, o := range st.Operations {
			o.State = "failed"
			o.Error = "offline"
			ops = append(ops, storage.Put("operations", o.ID, o))
		}
		return ops, nil
	})
	w = req(h, "GET", "/api/v1/messages?mailbox=inbox", "", nil, cookie, "")
	var page struct {
		Items []domain.Message `json:"items"`
	}
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].UID != 9 {
		t.Fatal("failed move not restored", w.Body.String())
	}
}

func TestClearFailedOperationsProtectsActiveWorkAndMessages(t *testing.T) {
	s, h := testServer(t)
	s.App.Close()
	cookie, csrf := loginTest(t, h)
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		ops := []storage.Op{storage.Put("messages", "m", domain.Message{ID: "m", UID: 42, UIDValidity: 7, Seen: true, ContentID: "keep-content"})}
		for _, state := range []string{"failed", "queued", "running", "done"} {
			ops = append(ops, storage.Put("operations", state, domain.Operation{ID: state, MessageID: "m", Kind: "move", State: state}))
		}
		ops = append(ops, storage.Put("operations", "failed-two", domain.Operation{ID: "failed-two", MessageID: "m", Kind: "seen", State: "failed"}))
		return ops, nil
	})
	for _, id := range []string{"queued", "running", "done"} {
		w := req(h, "DELETE", "/api/v1/operations/"+id, "http://localhost:8080", nil, cookie, csrf)
		if w.Code != 400 {
			t.Fatal("nonfailed operation was removed", id, w.Code)
		}
	}
	w := req(h, "DELETE", "/api/v1/operations/failed", "http://localhost:8080", nil, cookie, "")
	if w.Code != 403 {
		t.Fatal("clear bypassed CSRF", w.Code)
	}
	for n := 0; n < 2; n++ {
		w = req(h, "DELETE", "/api/v1/operations/failed", "http://localhost:8080", nil, cookie, csrf)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w = req(h, "DELETE", "/api/v1/operations", "http://localhost:8080", nil, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Removed int `json:"removed"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Removed != 1 {
		t.Fatal(w.Body.String())
	}
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		if len(st.Operations) != 3 || st.Operations["queued"].State != "queued" || st.Operations["running"].State != "running" {
			t.Fatal("active work cleared", st.Operations)
		}
		m := st.Messages["m"]
		if m.UID != 42 || !m.Seen || m.ContentID != "keep-content" {
			t.Fatal("clear changed mail", m)
		}
		if len(st.SyncTasks) != 0 {
			t.Fatal("clear unexpectedly queued remote work")
		}
	})
}

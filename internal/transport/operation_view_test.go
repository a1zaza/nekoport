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

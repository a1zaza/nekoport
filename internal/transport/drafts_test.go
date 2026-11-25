package transport

import (
	"encoding/json"
	"testing"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

func TestSaveDraftAcceptsLegacyEmptyTimestampAndOwnsMetadata(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginTest(t, h)
	for _, timestamp := range []string{"", "not-a-time", "2000-01-01T00:00:00Z"} {
		payload := map[string]any{"id": "", "accountId": "test-account", "to": "me@example.test", "cc": "", "bcc": "", "subject": "1234", "bodyId": "client-body", "attachments": []domain.Attachment{{ID: "client-attachment"}}, "revision": 0, "updatedAt": timestamp, "body": "12341"}
		w := req(h, "POST", "/api/v1/drafts", "http://localhost:8080", payload, cookie, csrf)
		if w.Code != 200 {
			t.Fatalf("timestamp %q: %d %s", timestamp, w.Code, w.Body.String())
		}
		var draft domain.Draft
		if err := json.Unmarshal(w.Body.Bytes(), &draft); err != nil {
			t.Fatal(err)
		}
		if draft.ID == "" || draft.Revision != 1 || draft.UpdatedAt.IsZero() || draft.UpdatedAt.Year() == 2000 || draft.BodyID == "client-body" || len(draft.Attachments) != 0 {
			t.Fatalf("invalid server-owned metadata: %+v", draft)
		}
		body, err := s.App.Store.LoadObject(draft.BodyID)
		if err != nil || string(body) != "12341" {
			t.Fatal("body not saved", err)
		}
		// Existing attachment metadata must survive an old client's stale snapshot.
		s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
			d := st.Drafts[draft.ID]
			d.Attachments = []domain.Attachment{{ID: "server-attachment", Name: "saved.txt"}}
			return []storage.Op{storage.Put("drafts", d.ID, d)}, nil
		})
		payload["id"] = draft.ID
		payload["revision"] = draft.Revision
		payload["body"] = "修改后的正文"
		w = req(h, "PUT", "/api/v1/drafts/"+draft.ID, "http://localhost:8080", payload, cookie, csrf)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var updated domain.Draft
		if err = json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
			t.Fatal(err)
		}
		if updated.Revision != 2 || len(updated.Attachments) != 1 || updated.Attachments[0].ID != "server-attachment" {
			t.Fatal("metadata overwritten", updated)
		}
		// Same request with the old revision must still conflict.
		w = req(h, "PUT", "/api/v1/drafts/"+draft.ID, "http://localhost:8080", payload, cookie, csrf)
		if w.Code != 409 {
			t.Fatal("stale revision accepted", w.Code, w.Body.String())
		}
		w = req(h, "GET", "/api/v1/drafts/"+draft.ID, "", nil, cookie, "")
		var loaded struct {
			Draft domain.Draft `json:"draft"`
			Body  string       `json:"body"`
		}
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if err = json.Unmarshal(w.Body.Bytes(), &loaded); err != nil {
			t.Fatal(err)
		}
		if loaded.Body != "修改后的正文" || loaded.Draft.Revision != 2 {
			t.Fatal("draft readback mismatch", loaded)
		}
	}
}

func TestSaveDraftMinimalInputAndStrictValidation(t *testing.T) {
	_, h := testServer(t)
	cookie, csrf := loginTest(t, h)
	payload := map[string]any{"accountId": "test-account", "to": "me@example.test", "subject": "主题", "body": "正文", "revision": 0}
	w := req(h, "POST", "/api/v1/drafts", "http://localhost:8080", payload, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	payload["unexpected"] = true
	w = req(h, "POST", "/api/v1/drafts", "http://localhost:8080", payload, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("unknown field accepted", w.Code)
	}
	delete(payload, "unexpected")
	payload["revision"] = "invalid"
	w = req(h, "POST", "/api/v1/drafts", "http://localhost:8080", payload, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("invalid mutable field accepted", w.Code)
	}
}

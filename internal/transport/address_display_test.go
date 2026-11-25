package transport

import (
	"encoding/json"
	"mime"
	"net/url"
	"testing"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

func TestExistingMessageAndOutboxReadableWithoutResync(t *testing.T) {
	s, h := testServer(t)
	encoded := mime.BEncoding.Encode("UTF-8", "项目团队") + " <team@example.test>"
	content, _ := json.Marshal(domain.Content{Text: "正文", Attachments: []domain.Attachment{}})
	object, err := s.App.Store.Object(content)
	if err != nil {
		t.Fatal(err)
	}
	err = s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		account := st.Accounts["test-account"]
		account.Name = "发信账号"
		account.Address = "self@example.test"
		return []storage.Op{storage.Put("accounts", account.ID, account), storage.Put("mailboxes", "inbox", domain.Mailbox{ID: "inbox", AccountID: account.ID, Role: "inbox"}), storage.Put("messages", "m", domain.Message{ID: "m", AccountID: account.ID, MailboxID: "inbox", From: encoded, ReplyTo: encoded, To: `"Doe, Jane" <jane@example.test>`, ContentID: object}), storage.Put("jobs", "j", domain.SendJob{ID: "j", AccountID: account.ID, To: encoded, State: "sent"})}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := loginTest(t, h)
	w := req(h, "GET", "/api/v1/messages?q="+url.QueryEscape("项目团队"), "", nil, cookie, "")
	var list struct {
		Items []domain.Message `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if w.Code != 200 || len(list.Items) != 1 || list.Items[0].FromName != "项目团队" || list.Items[0].From != encoded {
		t.Fatal(w.Code, w.Body.String())
	}
	w = req(h, "GET", "/api/v1/messages/m", "", nil, cookie, "")
	var detail struct {
		Message domain.Message `json:"message"`
	}
	json.Unmarshal(w.Body.Bytes(), &detail)
	if w.Code != 200 || detail.Message.FromDisplay != "项目团队 <team@example.test>" || detail.Message.ReplyTo != encoded {
		t.Fatal(w.Code, w.Body.String())
	}
	w = req(h, "GET", "/api/v1/bootstrap", "", nil, cookie, "")
	var bootstrap struct {
		Jobs []domain.SendJob `json:"jobs"`
	}
	json.Unmarshal(w.Body.Bytes(), &bootstrap)
	if w.Code != 200 || len(bootstrap.Jobs) != 1 || bootstrap.Jobs[0].ToDisplay != "项目团队 <team@example.test>" || bootstrap.Jobs[0].FromDisplay != "发信账号 <self@example.test>" {
		t.Fatal(w.Body.String())
	}
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Messages["m"].FromDisplay != "" || st.Jobs["j"].To != encoded {
			t.Fatal("view rendering changed stored data")
		}
	})
}

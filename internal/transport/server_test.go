package transport

import (
	"bytes"
	"encoding/json"
	"github.com/a1zaza/nekoport/internal/app"
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func testServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	st, e := storage.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	key := filepath.Join(dir, "key")
	secrets.Generate(key)
	v, e := secrets.Open(filepath.Join(dir, "secrets.json"), key)
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	st.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		s.Settings.Username = "test-user"
		s.Settings.PasswordHash = string(hash)
		return []storage.Op{storage.Put("settings", "", s.Settings), storage.Put("accounts", "test-account", domain.Account{ID: "test-account", CredentialRef: "private-secret-ref"})}, nil
	})
	a := app.New(st, v)
	t.Cleanup(func() { a.Close(); st.Close() })
	server := New(a, http.NotFoundHandler(), nil)
	return server, server.Handler()
}
func req(h http.Handler, method, path, origin string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, "http://localhost:8080"+path, &b)
	r.Header.Set("Origin", origin)
	r.Header.Set("X-CSRF-Token", csrf)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestSessionCSRFAndRedaction(t *testing.T) {
	_, h := testServer(t)
	if w := req(h, "GET", "/api/v1/bootstrap", "", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := req(h, "POST", "/api/v1/login", "http://localhost:8080", map[string]string{"username": "test-user", "password": "test-password"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	w = req(h, "GET", "/api/v1/bootstrap", "", nil, cookie, "")
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("private-secret-ref")) || bytes.Contains(w.Body.Bytes(), []byte("passwordHash")) {
		t.Fatal("secret leaked", w.Body.String())
	}
	settings := map[string]any{"fontSize": 16, "autoRead": true, "cacheMiB": 1024}
	if w = req(h, "PUT", "/api/v1/settings", "http://localhost:8080", settings, cookie, ""); w.Code != 403 {
		t.Fatal("CSRF bypassed")
	}
	if w = req(h, "PUT", "/api/v1/settings", "http://evil.test", settings, cookie, result["csrf"]); w.Code != 403 {
		t.Fatal("origin bypassed")
	}
	if w = req(h, "PUT", "/api/v1/settings", "http://localhost:8080", settings, cookie, result["csrf"]); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	req(h, "POST", "/api/v1/logout", "http://localhost:8080", nil, cookie, result["csrf"])
	if w = req(h, "GET", "/api/v1/bootstrap", "", nil, cookie, ""); w.Code != 401 {
		t.Fatal("logout did not revoke")
	}
}
func TestRejectDNSRebindingHost(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "http://evil.test/api/v1/session", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted host allowed")
	}
}
func TestCursorAndChineseSearch(t *testing.T) {
	s, h := testServer(t)
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for n := 0; n < 60; n++ {
			id := storage.ID()
			ops = append(ops, storage.Put("messages", id, domain.Message{ID: id, AccountID: "test-account", Subject: "中文通知"}))
		}
		return ops, nil
	})
	w := req(h, "POST", "/api/v1/login", "http://localhost:8080", map[string]string{"username": "test-user", "password": "test-password"}, nil, "")
	cookie := w.Result().Cookies()[0]
	w = req(h, "GET", "/api/v1/messages?q=%E4%B8%AD%E6%96%87", "", nil, cookie, "")
	var first struct {
		Items  []domain.Message `json:"items"`
		Cursor string           `json:"nextCursor"`
	}
	json.Unmarshal(w.Body.Bytes(), &first)
	if len(first.Items) != 50 || first.Cursor == "" {
		t.Fatal(w.Body.String())
	}
	w = req(h, "GET", "/api/v1/messages?cursor="+first.Cursor, "", nil, cookie, "")
	var second struct {
		Items []domain.Message `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &second)
	if len(second.Items) != 10 {
		t.Fatal("unstable cursor", w.Body.String())
	}
}

func TestSharedCredentialsPreserveSavedIMAPSecret(t *testing.T) {
	s, _ := testServer(t)
	ref, e := s.App.Vault.Put(secrets.Credentials{IMAP: "imap-existing", SMTP: "smtp-different"})
	if e != nil {
		t.Fatal(e)
	}
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		a := st.Accounts["test-account"]
		a.CredentialRef = ref
		return []storage.Op{storage.Put("accounts", a.ID, a)}, nil
	})
	in := accountInput{Account: domain.Account{ID: "test-account", IMAP: domain.Endpoint{Username: "imap-login"}, SMTP: domain.Endpoint{Username: "different-login"}}, UseSameCredentials: true}
	if e = s.sharedCredentials(&in); e != nil {
		t.Fatal(e)
	}
	if in.SMTPPassword != "imap-existing" || in.SMTP.Username != "imap-login" {
		t.Fatal("shared credentials did not reuse existing IMAP secret")
	}
}

func TestForwardPreservesAttachmentsInProtectedDraft(t *testing.T) {
	s, h := testServer(t)
	at, e := s.App.Store.Object([]byte("附件原文"))
	if e != nil {
		t.Fatal(e)
	}
	content, _ := json.Marshal(domain.Content{Text: "原始正文", HTML: "<b>原始正文</b>", Attachments: []domain.Attachment{{ID: at, Name: "资料.txt", Size: 12}}})
	contentID, e := s.App.Store.Object(content)
	if e != nil {
		t.Fatal(e)
	}
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("messages", "original", domain.Message{ID: "original", AccountID: "test-account", ContentID: contentID, Subject: "原始主题", From: "from@example.test"})}, nil
	})
	login := req(h, "POST", "/api/v1/login", "http://localhost:8080", map[string]string{"username": "test-user", "password": "test-password"}, nil, "")
	var session map[string]string
	json.Unmarshal(login.Body.Bytes(), &session)
	w := req(h, "POST", "/api/v1/messages/original/forward", "http://localhost:8080", nil, login.Result().Cookies()[0], session["csrf"])
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Draft domain.Draft `json:"draft"`
		Body  string       `json:"body"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Draft.Subject != "Fwd: 原始主题" || len(result.Draft.Attachments) != 1 || result.Draft.Attachments[0].ID != at || !bytes.Contains([]byte(result.Body), []byte("原始正文")) {
		t.Fatal(w.Body.String())
	}
}

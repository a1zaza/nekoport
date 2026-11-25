package transport

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

func loginTest(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := req(h, "POST", "/api/v1/login", "http://localhost:8080", map[string]string{"username": "test-user", "password": "test-password"}, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	return w.Result().Cookies()[0], data.CSRF
}
func TestChangeAccountRevokeAndKeepPreferences(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginTest(t, h)
	other, _ := loginTest(t, h)
	input := map[string]any{"username": "new-user", "currentPassword": "wrong", "expectedRevision": 1}
	w := req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", input, cookie, csrf)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	input["currentPassword"] = "test-password"
	input["newPassword"] = "新的密码123456"
	if w = req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", input, cookie, ""); w.Code != 403 {
		t.Fatal("csrf accepted")
	}
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		v := st.Settings
		v.FontSize = 22
		return []storage.Op{storage.Put("settings", "", v)}, nil
	})
	s.mu.Lock()
	oldEvents := s.authEvents
	s.mu.Unlock()
	w = req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", input, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-oldEvents:
	default:
		t.Fatal("SSE not revoked")
	}
	if w = req(h, "GET", "/api/v1/bootstrap", "", nil, other, ""); w.Code != 401 {
		t.Fatal("other session survived")
	}
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Settings.FontSize != 22 || st.Settings.AuthRevision != 2 {
			t.Fatal("settings overwritten")
		}
	})
	if w = req(h, "POST", "/api/v1/login", "http://localhost:8080", map[string]string{"username": "new-user", "password": "新的密码123456"}, nil, ""); w.Code != 200 {
		t.Fatal("new credentials unusable", w.Body.String())
	}
}
func TestAccountValidationAndFailurePreserveSession(t *testing.T) {
	cases := []struct {
		name     string
		password any
		username string
		revision int
		code     int
	}{
		{"no change", nil, "test-user", 1, 422}, {"same password", "test-password", "test-user", 1, 422}, {"short", "123", "test-user", 1, 422}, {"too many bytes", "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十", "test-user", 1, 422}, {"control", nil, "new\nuser", 1, 422}, {"stale", nil, "new-user", 9, 409},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, h := testServer(t)
			cookie, csrf := loginTest(t, h)
			input := map[string]any{"username": tt.username, "currentPassword": "test-password", "expectedRevision": tt.revision}
			if tt.password != nil {
				input["newPassword"] = tt.password
			}
			w := req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", input, cookie, csrf)
			if w.Code != tt.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if req(h, "GET", "/api/v1/bootstrap", "", nil, cookie, "").Code != 200 {
				t.Fatal("failure revoked session")
			}
		})
	}
	t.Run("write failure", func(t *testing.T) {
		s, h := testServer(t)
		cookie, csrf := loginTest(t, h)
		s.App.Store.Close()
		w := req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", map[string]any{"username": "new-user", "currentPassword": "test-password", "expectedRevision": 1}, cookie, csrf)
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
		if _, ok := s.session(&http.Request{Header: http.Header{"Cookie": []string{cookie.String()}}}); !ok {
			t.Fatal("failed write revoked session")
		}
	})
}
func TestConcurrentLoginCannotSurviveAccountChange(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginTest(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req(h, "POST", "/api/v1/login", "http://localhost:8080", map[string]string{"username": "test-user", "password": "test-password"}, nil, "")
		}()
	}
	w := req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", map[string]any{"username": "new-user", "currentPassword": "test-password", "expectedRevision": 1}, cookie, csrf)
	wg.Wait()
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.sessions {
		if session.Revision < 2 {
			t.Fatal("old login survived mutation")
		}
	}
}

func TestAccountChangeThrottleAndSettingsFieldIsolation(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginTest(t, h)
	for n := 0; n < 6; n++ {
		w := req(h, "PUT", "/api/v1/auth/account", "http://localhost:8080", map[string]any{"username": "new-user", "currentPassword": "wrong", "expectedRevision": 1}, cookie, csrf)
		want := 403
		if n == 5 {
			want = 429
		}
		if w.Code != want {
			t.Fatal("limit mismatch", n, w.Code)
		}
	}
	w := req(h, "PUT", "/api/v1/settings", "http://localhost:8080", map[string]any{"fontSize": 15, "autoRead": true, "cacheMiB": 1024, "authRevision": 7, "passwordHash": "injected"}, cookie, csrf)
	if w.Code != 400 {
		t.Fatal("settings accepted auth fields", w.Code)
	}
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Settings.Username != "test-user" || st.Settings.PasswordHash == "injected" {
			t.Fatal("auth state overwritten")
		}
	})
}

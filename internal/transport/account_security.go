package transport

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

func authRevision(v int) int {
	if v < 1 {
		return 1
	}
	return v
}
func (s *Server) changeAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string  `json:"username"`
		Current  string  `json:"currentPassword"`
		New      *string `json:"newPassword"`
		Revision int     `json:"expectedRevision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := decode(w, r, &in); err != nil {
		failure(w, err)
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	// Recheck after acquiring the login/update barrier.
	if _, ok := s.session(r); !ok {
		JSON(w, 401, map[string]string{"message": "请重新登录"})
		return
	}
	var current domain.Settings
	s.App.Store.Read(func(st domain.State, _ storage.Index) { current = st.Settings })
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := "account:" + ip
	s.mu.Lock()
	limit := s.attempts[key]
	s.mu.Unlock()
	if limit.Count >= 5 && time.Now().Before(limit.Until) {
		JSON(w, 429, map[string]string{"message": "验证失败次数过多，请 5 分钟后重试"})
		return
	}
	if len(in.Current) > 4096 || bcrypt.CompareHashAndPassword([]byte(current.PasswordHash), []byte(in.Current)) != nil {
		s.mu.Lock()
		if time.Now().After(limit.Until) {
			limit = throttle{Until: time.Now().Add(5 * time.Minute)}
		}
		limit.Count++
		s.attempts[key] = limit
		s.mu.Unlock()
		JSON(w, 403, map[string]string{"message": "当前密码不正确"})
		return
	}
	invalid := func(msg string) { JSON(w, 422, map[string]string{"message": msg}) }
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || len(in.Username) > 64 || strings.ContainsFunc(in.Username, unicode.IsControl) {
		invalid("用户名应为 1–64 字节，不能包含控制字符")
		return
	}
	hash := current.PasswordHash
	if in.New != nil {
		if !utf8.ValidString(*in.New) || utf8.RuneCountInString(*in.New) < 8 || len(*in.New) > 72 {
			invalid("新密码至少 8 个字符，最多 72 字节")
			return
		}
		if *in.New == in.Current {
			invalid("新密码不能与当前密码相同")
			return
		}
		b, err := bcrypt.GenerateFromPassword([]byte(*in.New), bcrypt.DefaultCost)
		if err != nil {
			invalid("新密码格式无效")
			return
		}
		hash = string(b)
	} else if in.Username == current.Username {
		invalid("请修改用户名或密码")
		return
	}
	err := s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		v := st.Settings
		if authRevision(v.AuthRevision) != in.Revision || v.PasswordHash != current.PasswordHash {
			return nil, storage.ErrConflict
		}
		v.Username = in.Username
		v.PasswordHash = hash
		v.AuthRevision = authRevision(v.AuthRevision) + 1
		return []storage.Op{storage.Put("settings", "", v)}, nil
	})
	if err != nil {
		if !s.App.Store.Writable() {
			JSON(w, 503, map[string]string{"message": "存储不可写，账户未确认更新"})
		} else if errors.Is(err, storage.ErrConflict) {
			failure(w, err)
		} else {
			failure(w, err)
		}
		return
	}
	s.mu.Lock()
	s.sessions = map[string]session{}
	delete(s.attempts, key)
	close(s.authEvents)
	s.authEvents = make(chan struct{})
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "nekoport_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.App.Changed()
	JSON(w, 200, map[string]bool{"reauthenticate": true})
}

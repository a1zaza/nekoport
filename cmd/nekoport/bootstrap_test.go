package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/a1zaza/nekoport/internal/app"
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/a1zaza/nekoport/internal/transport"
	"golang.org/x/crypto/bcrypt"
)

func consolePassword(t *testing.T, console *bytes.Buffer) string {
	t.Helper()
	if strings.Count(console.String(), "用户名：admin") != 1 {
		t.Fatal("missing one-time username")
	}
	for _, line := range strings.Split(console.String(), "\n") {
		if password, ok := strings.CutPrefix(line, "密码："); ok {
			entropy, err := base64.RawURLEncoding.DecodeString(password)
			if err != nil || len(entropy) != 24 || len(password) != 32 {
				t.Fatal("invalid random password format")
			}
			return password
		}
	}
	t.Fatal("missing one-time password")
	return ""
}

func TestDefaultAccountConsoleOnlyAndPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := openCredentialVault(store, "")
	if err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	if err = bootstrapDefaultAccount(store, &console); err != nil {
		t.Fatal(err)
	}
	password := consolePassword(t, &console)
	var settings domain.Settings
	store.Read(func(state domain.State, _ storage.Index) { settings = state.Settings })
	if settings.Username != "admin" || settings.AuthRevision != 1 || bcrypt.CompareHashAndPassword([]byte(settings.PasswordHash), []byte(password)) != nil {
		t.Fatal("invalid persisted account")
	}
	// Exercise the real login endpoint without a running service or mailbox.
	application := app.New(store, vault)
	handler := transport.New(application, http.NotFoundHandler(), nil).Handler()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	request := httptest.NewRequest("POST", "http://localhost:8080/api/v1/login", bytes.NewReader(body))
	request.Header.Set("Origin", "http://localhost:8080")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != 200 || len(result.Result().Cookies()) != 1 {
		t.Fatal("generated account cannot log in")
	}
	application.Close()
	if err = store.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == "dev-account.json" {
			t.Fatal("plaintext credential file created")
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte(password)) {
			t.Fatal("plaintext password persisted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var repeated bytes.Buffer
	if err = bootstrapDefaultAccount(reopened, &repeated); err != nil {
		t.Fatal(err)
	}
	if repeated.Len() != 0 {
		t.Fatal("password printed on restart")
	}
	reopened.Read(func(state domain.State, _ storage.Index) {
		if state.Settings.Username != settings.Username || state.Settings.PasswordHash != settings.PasswordHash {
			t.Fatal("existing account replaced")
		}
	})
}

func TestCustomAccountIsNeverResetOrPrinted(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("test-changed-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(state domain.State, _ storage.Index) ([]storage.Op, error) {
		settings := state.Settings
		settings.Username = "renamed"
		settings.PasswordHash = string(hash)
		settings.AuthRevision = 3
		return []storage.Op{storage.Put("settings", "", settings)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	if err = bootstrapDefaultAccount(store, &console); err != nil {
		t.Fatal(err)
	}
	if console.Len() != 0 {
		t.Fatal("existing account printed")
	}
	store.Read(func(state domain.State, _ storage.Index) {
		if state.Settings.Username != "renamed" || state.Settings.PasswordHash != string(hash) || state.Settings.AuthRevision != 3 {
			t.Fatal("custom account replaced")
		}
	})
}

func TestCorruptOrPreviouslyUsedStateDoesNotBootstrap(t *testing.T) {
	for _, name := range []string{"missing hash", "missing username", "bad hash", "existing data", "existing revision"} {
		t.Run(name, func(t *testing.T) {
			store, err := storage.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			err = store.Update(func(state domain.State, _ storage.Index) ([]storage.Op, error) {
				settings := state.Settings
				switch name {
				case "missing hash":
					settings.Username = "admin"
				case "missing username":
					settings.PasswordHash = "old"
				case "bad hash":
					settings.Username = "admin"
					settings.PasswordHash = "invalid"
				case "existing data":
					return []storage.Op{storage.Put("accounts", "old", domain.Account{ID: "old"})}, nil
				case "existing revision":
					settings.AuthRevision = 2
				}
				return []storage.Op{storage.Put("settings", "", settings)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var console bytes.Buffer
			if err = bootstrapDefaultAccount(store, &console); err == nil {
				t.Fatal("unsafe account initialization accepted")
			}
			if console.Len() != 0 {
				t.Fatal("credentials printed for corrupt state")
			}
		})
	}
}

func TestPortFailureDoesNotConsumeFirstPassword(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var console bytes.Buffer
	if listener, err := listenAndBootstrap(store, occupied.Addr().String(), &console); err == nil {
		listener.Close()
		t.Fatal("occupied port accepted")
	}
	if console.Len() != 0 {
		t.Fatal("password printed for failed listener")
	}
	store.Read(func(state domain.State, _ storage.Index) {
		if state.Settings.Username != "" || state.Settings.PasswordHash != "" {
			t.Fatal("failed bind consumed initialization")
		}
	})
	listener, err := listenAndBootstrap(store, "127.0.0.1:0", &console)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	consolePassword(t, &console)
}

func TestFailedWriteDoesNotPrintCredentials(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	var console bytes.Buffer
	if err = bootstrapDefaultAccount(store, &console); err == nil {
		t.Fatal("closed store accepted write")
	}
	if console.Len() != 0 {
		t.Fatal("uncommitted password printed")
	}
}

func TestConcurrentBootstrapPrintsOnlyWinningPassword(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var console bytes.Buffer
	results := make(chan error, 4)
	var start sync.WaitGroup
	start.Add(1)
	for n := 0; n < cap(results); n++ {
		go func() { start.Wait(); results <- bootstrapDefaultAccount(store, &console) }()
	}
	start.Done()
	for n := 0; n < cap(results); n++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	password := consolePassword(t, &console)
	store.Read(func(state domain.State, _ storage.Index) {
		if bcrypt.CompareHashAndPassword([]byte(state.Settings.PasswordHash), []byte(password)) != nil {
			t.Fatal("printed password was not committed")
		}
	})
}

type failedConsole struct{}

func (failedConsole) Write([]byte) (int, error) { return 0, errors.New("console unavailable") }
func TestConsoleFailureDoesNotResetCommittedAccount(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = bootstrapDefaultAccount(store, failedConsole{}); err == nil {
		t.Fatal("console error hidden")
	}
	var console bytes.Buffer
	if err = bootstrapDefaultAccount(store, &console); err != nil {
		t.Fatal(err)
	}
	if console.Len() != 0 {
		t.Fatal("committed account regenerated after output failure")
	}
}

type shortConsole struct{}

func (shortConsole) Write([]byte) (int, error) { return 1, nil }
func TestShortConsoleWriteReturnsError(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = bootstrapDefaultAccount(store, shortConsole{}); err == nil {
		t.Fatal("short console write hidden")
	}
}

package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedCredentialsAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if e := Generate(key); e != nil {
		t.Fatal(e)
	}
	if Generate(key) == nil {
		t.Fatal("overwrote key")
	}
	path := filepath.Join(dir, "secrets.json")
	v, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	id, e := v.Put(Credentials{IMAP: "test-secret-imap", SMTP: "test-secret-smtp"})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "test-secret") {
		t.Fatal("plaintext secret on disk")
	}
	v, e = Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	c, e := v.Get(id)
	if e != nil || c.SMTP != "test-secret-smtp" {
		t.Fatal(c, e)
	}
	other := filepath.Join(dir, "other")
	Generate(other)
	if _, e = Open(path, other); e == nil {
		t.Fatal("wrong key accepted")
	}
}

func TestEnsureKeyInitializesOnlySafeMissingKeys(t *testing.T) {
	for _, test := range []struct {
		name, vault, key string
		allow            bool
	}{
		{name: "fresh", allow: true},
		{name: "empty vault", vault: `{"schemaVersion":1,"entries":{}}`, allow: true},
		{name: "encrypted vault", vault: `{"schemaVersion":1,"entries":{"id":"encrypted"}}`},
		{name: "corrupt vault", vault: `{invalid`},
		{name: "unknown version", vault: `{"schemaVersion":2,"entries":{}}`},
		{name: "empty existing key", key: ""},
		{name: "invalid existing key", key: "invalid"},
		{name: "oversized existing key", key: strings.Repeat("A", 257)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			key := filepath.Join(dir, "master.key")
			vault := filepath.Join(dir, "secrets.json")
			if test.vault != "" {
				if err := os.WriteFile(vault, []byte(test.vault), 0600); err != nil {
					t.Fatal(err)
				}
			}
			existing := strings.Contains(test.name, "existing key")
			if existing {
				if err := os.WriteFile(key, []byte(test.key), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := EnsureKey(vault, key)
			if !test.allow {
				if err == nil {
					t.Fatal("unsafe initialization accepted")
				}
				if existing {
					data, _ := os.ReadFile(key)
					if string(data) != test.key {
						t.Fatal("existing invalid key overwritten")
					}
				} else if _, err = os.Lstat(key); !os.IsNotExist(err) {
					t.Fatal("replacement key created")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(key)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(key)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("incorrect key permissions")
			}
			if _, err = Open(vault, key); err != nil {
				t.Fatal(err)
			}
			if err = EnsureKey(vault, key); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(key)
			if string(before) != string(after) {
				t.Fatal("key changed on repeat initialization")
			}
		})
	}
}

func TestAtomicConcurrentKeyInitialization(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	vault := filepath.Join(dir, "secrets.json")
	results := make(chan error, 32)
	for n := 0; n < cap(results); n++ {
		go func() { results <- EnsureKey(vault, key) }()
	}
	for n := 0; n < cap(results); n++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(vault, key); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "master.key" {
		t.Fatal("temporary key leak", err)
	}
}

func TestMissingKeyCannotReplaceEncryptedCredentials(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	vaultPath := filepath.Join(dir, "secrets.json")
	if err := EnsureKey(vaultPath, key); err != nil {
		t.Fatal(err)
	}
	vault, err := Open(vaultPath, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = vault.Put(Credentials{IMAP: "test-only-secret"}); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err = EnsureKey(vaultPath, key); err == nil {
		t.Fatal("lost key silently replaced")
	}
	if _, err = os.Lstat(key); !os.IsNotExist(err) {
		t.Fatal("replacement file left behind")
	}
}

func TestKeyDirectoryIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	if err := os.Mkdir(key, 0700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureKey(filepath.Join(dir, "secrets.json"), key); err == nil {
		t.Fatal("directory treated as a key")
	}
	info, err := os.Stat(key)
	if err != nil || !info.IsDir() {
		t.Fatal("existing directory replaced")
	}
}

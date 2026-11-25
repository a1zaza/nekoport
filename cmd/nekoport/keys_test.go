package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
)

func TestDefaultKeyAndExplicitReadOnlyKey(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "data directory", true: "explicit path"}[explicit], func(t *testing.T) {
			dir := t.TempDir()
			store, err := storage.Open(filepath.Join(dir, "data"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			key := filepath.Join(store.Root(), "master.key")
			argument := ""
			if explicit {
				key = filepath.Join(dir, "external.key")
				argument = key
				if err = secrets.Generate(key); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(key, 0400); err != nil {
					t.Fatal(err)
				}
			}
			vault, err := openCredentialVault(store, argument)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(key)
			if err != nil {
				t.Fatal(err)
			}
			id, err := vault.Put(secrets.Credentials{IMAP: "test-only-value"})
			if err != nil {
				t.Fatal(err)
			}
			again, err := openCredentialVault(store, argument)
			if err != nil {
				t.Fatal(err)
			}
			credentials, err := again.Get(id)
			if err != nil || credentials.IMAP != "test-only-value" {
				t.Fatal("credential lost", err)
			}
			after, _ := os.ReadFile(key)
			if string(before) != string(after) {
				t.Fatal("key replaced")
			}
			if explicit {
				info, _ := os.Stat(key)
				if info.Mode().Perm() != 0400 {
					t.Fatal("read-only key changed")
				}
				if _, err = os.Stat(filepath.Join(store.Root(), "master.key")); !os.IsNotExist(err) {
					t.Fatal("unexpected default key")
				}
			}
		})
	}
}

func TestCredentialReferencesBlockKeyRegeneration(t *testing.T) {
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.Update(func(_ domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", "a", domain.Account{ID: "a", CredentialRef: "existing"})}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = openCredentialVault(store, ""); err == nil {
		t.Fatal("missing vault and key ignored")
	}
	if _, err = os.Stat(filepath.Join(store.Root(), "master.key")); !os.IsNotExist(err) {
		t.Fatal("replacement key generated")
	}
}

func TestInitAutomaticallyCreatesDefaultKey(t *testing.T) {
	dir := t.TempDir()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = original; reader.Close() }()
	if _, err = writer.WriteString("test-only-password\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	args := []string{"init", "-data", dir, "-username", "owner", "-password-stdin"}
	if err = run(args); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "master.key")
	before, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if err = run(args); err == nil {
		t.Fatal("existing account overwritten")
	}
	after, _ := os.ReadFile(key)
	if string(before) != string(after) {
		t.Fatal("repeat init replaced key")
	}
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var console bytes.Buffer
	if err = bootstrapDefaultAccount(store, &console); err != nil {
		t.Fatal(err)
	}
	if console.Len() != 0 {
		t.Fatal("manual account replaced by bootstrap")
	}
	store.Read(func(state domain.State, _ storage.Index) {
		if state.Settings.Username != "owner" {
			t.Fatal("account not initialized")
		}
	})
}

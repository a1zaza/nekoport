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

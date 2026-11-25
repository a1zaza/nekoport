package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/server"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fixtureOnce sync.Once
var fixtureCert tls.Certificate
var fixtureCA []byte

type validityBackend struct {
	backend.Backend
	validity *atomic.Uint32
}

func (b *validityBackend) Login(info *imap.ConnInfo, username, password string) (backend.User, error) {
	u, e := b.Backend.Login(info, username, password)
	if e != nil {
		return nil, e
	}
	return &validityUser{User: u, validity: b.validity}, nil
}

type validityUser struct {
	backend.User
	validity *atomic.Uint32
}

func (u *validityUser) GetMailbox(name string) (backend.Mailbox, error) {
	b, e := u.User.GetMailbox(name)
	if e != nil {
		return nil, e
	}
	return &validityMailbox{Mailbox: b, validity: u.validity}, nil
}

type validityMailbox struct {
	backend.Mailbox
	validity *atomic.Uint32
}

func (b *validityMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	s, e := b.Mailbox.Status(items)
	if e == nil {
		s.UidValidity = b.validity.Load()
	}
	return s, e
}
func imapFixture(t *testing.T) (*App, domain.Account, secrets.Credentials, *memory.User) {
	a, x, c, u, _ := imapFixtureGeneration(t)
	return a, x, c, u
}
func imapFixtureGeneration(t *testing.T) (*App, domain.Account, secrets.Credentials, *memory.User, *atomic.Uint32) {
	t.Helper()
	fixtureOnce.Do(func() {
		key, e := rsa.GenerateKey(rand.Reader, 2048)
		if e != nil {
			t.Fatal(e)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "nekoport test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if e != nil {
			t.Fatal(e)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		cert, e := tls.X509KeyPair(certPEM, keyPEM)
		if e != nil {
			t.Fatal(e)
		}
		fixtureCert = cert
		fixtureCA = certPEM
	})
	cert, certPEM := fixtureCert, fixtureCA
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(caPath, certPEM, 0600)
	t.Setenv("SSL_CERT_FILE", caPath)
	t.Setenv("SSL_CERT_DIR", t.TempDir())
	backend := memory.New()
	user, e := backend.Login(nil, "username", "password")
	if e != nil {
		t.Fatal(e)
	}
	user.CreateMailbox("Sent")
	user.CreateMailbox("Trash")
	listener, e := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	if e != nil {
		t.Fatal(e)
	}
	validity := new(atomic.Uint32)
	validity.Store(1)
	srv := server.New(&validityBackend{Backend: backend, validity: validity})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	t.Cleanup(func() { srv.Close(); listener.Close(); <-done })
	a := testApp(t)
	port := listener.Addr().(*net.TCPAddr).Port
	var account domain.Account
	a.Store.Read(func(st domain.State, _ storage.Index) { account = st.Accounts["account"] })
	account.IMAP = domain.Endpoint{Host: "127.0.0.1", Port: port, TLS: "tls", Username: "username"}
	a.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", account.ID, account)}, nil
	})
	cred := secrets.Credentials{IMAP: "password"}
	ref, e := a.Vault.Put(cred)
	if e != nil {
		t.Fatal(e)
	}
	account.CredentialRef = ref
	a.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		return []storage.Op{storage.Put("accounts", account.ID, account)}, nil
	})

	return a, account, cred, user.(*memory.User), validity
}
func TestTLSIMAPSyncReadFlagsAndRemoteDeletion(t *testing.T) {
	a, account, cred, user := imapFixture(t)
	var e error
	if e = a.syncAccount(account, cred); e != nil {
		t.Fatal(e)
	}
	var msg domain.Message
	a.Store.Read(func(st domain.State, i storage.Index) {
		if len(i.All) != 1 {
			t.Fatalf("messages=%d", len(i.All))
		}
		msg = st.Messages[i.All[0]]
	})
	_, content, e := a.Content(msg.ID)
	if e != nil || !strings.Contains(content.Text, "Hi there") {
		t.Fatal(content, e)
	}
	if e = a.Operate([]string{msg.ID}, "seen", "", false); e != nil {
		t.Fatal(e)
	}
	a.processOperations(account, cred)
	if e = a.syncAccount(account, cred); e != nil {
		t.Fatal(e)
	}
	a.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Messages[msg.ID].Seen {
			t.Fatal("flag not synchronized")
		}
		for _, o := range st.Operations {
			if o.State != "done" {
				t.Fatal(o)
			}
		}
	})
	mb, e := user.GetMailbox("INBOX")
	if e != nil {
		t.Fatal(e)
	}
	mb.(*memory.Mailbox).Messages = nil
	if e = a.syncAccount(account, cred); e != nil {
		t.Fatal(e)
	}
	a.Store.Read(func(st domain.State, i storage.Index) {
		if len(st.Messages) != 0 || len(i.All) != 0 {
			t.Fatal("remote deletion not reconciled")
		}
	})
}

// Legacy integration entry point now exercises the production task engine. The
// fixture stays paused outside explicit reads so Operate cannot race this test.
func (a *App) syncAccount(x domain.Account, cred secrets.Credentials) error {
	deadline := time.Now().Add(time.Second)
	for a.Running()[x.ID] {
		if time.Now().After(deadline) {
			return errors.New("test worker did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	defer a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		v := s.Accounts[x.ID]
		v.Paused = true
		return []storage.Op{storage.Put("accounts", v.ID, v)}, nil
	})
	t := domain.SyncTask{ID: storage.ID(), Generation: storage.ID(), AccountID: x.ID, AccountRevision: x.Revision, Kind: "reconcile", State: "queued", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		v := s.Accounts[x.ID]
		v.Paused = false
		return []storage.Op{storage.Put("accounts", v.ID, v), storage.Put("syncTasks", t.ID, t)}, nil
	}); err != nil {
		return err
	}
	c, err := mailer.DialIMAP(context.Background(), x.IMAP, cred.IMAP)
	if err != nil {
		return err
	}
	defer c.Terminate()
	for n := 0; n < 100; n++ {
		a.Store.Read(func(s domain.State, _ storage.Index) { t = s.SyncTasks[t.ID].Clone() })
		if !t.Active() {
			if t.State != "succeeded" {
				return errors.New(t.Error)
			}
			return nil
		}
		if err = a.syncStep(c, x, t); err != nil {
			return err
		}
	}
	return errors.New("integration task did not finish")
}

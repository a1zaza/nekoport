package app

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
)

// Real TLS SMTP dialogues terminate locally; no test can contact a mail provider.
func smtpFixture(t *testing.T, mode string) (int, *atomic.Int32) {
	t.Helper()
	listener, e := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{fixtureCert}, MinVersion: tls.VersionTLS12})
	if e != nil {
		t.Fatal(e)
	}
	accepted := new(atomic.Int32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 test SMTP\r\n")
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-test\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(line, "AUTH"):
						fmt.Fprint(conn, "235 accepted\r\n")
					case strings.HasPrefix(line, "DATA"):
						fmt.Fprint(conn, "354 proceed\r\n")
						for {
							line, e = r.ReadString('\n')
							if e != nil {
								return
							}
							if line == ".\r\n" {
								break
							}
						}
						accepted.Add(1)
						if mode == "lost-final" {
							return
						}
						fmt.Fprint(conn, "250 accepted\r\n")
					case strings.HasPrefix(line, "QUIT"):
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						io.WriteString(conn, "250 ok\r\n")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.Addr().(*net.TCPAddr).Port, accepted
}

func TestSendingAppendsAndIndexesSentWithoutIDLE(t *testing.T) {
	for _, mode := range []string{"client-copy", "server-copy", "append-unknown", "lost-final"} {
		t.Run(mode, func(t *testing.T) {
			spy := &protocolSpy{failAppend: mode == "append-unknown"}
			a, x, _, _, _ := imapFixtureGeneration(t, spy.install)
			port, accepted := smtpFixture(t, mode)
			x.Paused = false
			x.SMTP = domain.Endpoint{Host: "127.0.0.1", Port: port, TLS: "tls", Username: "username"}
			x.AutoArchive = mode == "server-copy"
			ref, e := a.Vault.Put(secrets.Credentials{IMAP: "password", SMTP: "password"})
			if e != nil {
				t.Fatal(e)
			}
			x.CredentialRef = ref
			a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
			})
			draft, e := a.SaveDraft(domain.Draft{AccountID: x.ID, To: "recipient@example.test", Subject: "本地模拟发送"}, "正文")
			if e != nil {
				t.Fatal(e)
			}
			job, e := a.Queue(draft.ID, "0123456789abcdef0123456789abcdef", draft.Revision)
			if e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(6 * time.Second)
			var result domain.SendJob
			for {
				a.Store.Read(func(s domain.State, _ storage.Index) { result = s.Jobs[job.ID] })
				if (result.State == "sent" && result.Archive != "pending" && result.Archive != "submitting" || result.State == "unknown") && idleSettled(a, x.ID) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("send did not settle", result)
				}
				time.Sleep(10 * time.Millisecond)
			}
			appends := 0
			for _, call := range spy.snapshot() {
				if call.command == "APPEND" {
					appends++
				}
			}
			if accepted.Load() != 1 {
				t.Fatal("SMTP did not accept exactly once", accepted.Load())
			}
			if mode == "client-copy" || mode == "append-unknown" {
				expected := "saved"
				if mode == "append-unknown" {
					expected = "unknown"
				}
				if result.State != "sent" || result.Archive != expected || appends != 1 {
					t.Fatal("server copy missing", result, appends)
				}
				a.Store.Read(func(s domain.State, _ storage.Index) {
					found := false
					for _, m := range s.Messages {
						if m.Subject == draft.Subject && s.Mailboxes[m.MailboxID].Role == "sent" {
							found = true
						}
					}
					if !found {
						t.Fatal("APPEND succeeded without local Sent refresh")
					}
					if !s.SyncStatus[x.ID].LastFull.IsZero() {
						t.Fatal("send triggered account-wide reconciliation")
					}
				})
			} else if mode == "server-copy" {
				if result.Archive != "server" || appends != 0 {
					t.Fatal("server-copy mode duplicated APPEND", result, appends)
				}
			} else {
				if result.State != "unknown" || appends != 0 {
					t.Fatal("uncertain SMTP caused APPEND", result, appends)
				}
				a.Trigger(x.ID)
				time.Sleep(30 * time.Millisecond)
				if accepted.Load() != 1 {
					t.Fatal("uncertain SMTP replayed")
				}
			}
			if mode == "append-unknown" {
				a.Trigger(x.ID)
				time.Sleep(30 * time.Millisecond)
				n := 0
				for _, call := range spy.snapshot() {
					if call.command == "APPEND" {
						n++
					}
				}
				if n != 1 || accepted.Load() != 1 {
					t.Fatal("uncertain APPEND was replayed")
				}
			}
			a.Close()
		})
	}
}

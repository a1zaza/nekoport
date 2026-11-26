package mail

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

func TestIdleObserverUsesImmutableUIDsAndIgnoresBaseline(t *testing.T) {
	var notices []IdleNotice
	baseline := imap.NewMailboxStatus("INBOX", nil)
	baseline.UidValidity = 1
	baseline.UidNext = 7
	baseline.ReadOnly = true
	observe := newIdleObserver("INBOX", func(n IdleNotice) { notices = append(notices, n) }, baseline)
	observe(&imap.StatusResp{Tag: "*", Type: imap.StatusRespOk, Code: imap.CodeUidNext, Arguments: []interface{}{uint32(7)}})
	observe(&imap.StatusResp{Tag: "*", Type: imap.StatusRespOk, Code: imap.CodeUidValidity, Arguments: []interface{}{uint32(1)}})
	observe(&imap.StatusResp{Tag: "*", Type: imap.StatusRespOk, Code: imap.CodeReadOnly})
	if len(notices) != 0 {
		t.Fatal("SELECT baseline became data events", notices)
	}
	attrs := []interface{}{imap.RawString("UID"), uint32(42), imap.RawString("FLAGS"), []interface{}{imap.SeenFlag}}
	observe(&imap.DataResp{Fields: []interface{}{uint32(9), "FETCH", attrs}})
	attrs[1] = uint32(99)
	if len(notices) != 1 || notices[0].Kind != IdleFlags || notices[0].UID != 42 || notices[0].UIDValidity != 1 {
		t.Fatal("FETCH lost immutable UID identity", notices)
	}
	observe(&imap.DataResp{Fields: []interface{}{uint32(9), "EXPUNGE"}})
	observe(&imap.DataResp{Fields: []interface{}{uint32(10), "EXISTS"}})
	observe(&imap.StatusResp{Tag: "*", Type: imap.StatusRespOk, Code: imap.CodeReadWrite})
	if notices[1].Kind != IdleReconcile || notices[2].Kind != IdleNewMail || notices[3].Kind != IdleCapabilities {
		t.Fatal(notices)
	}
	observe(&imap.StatusResp{Tag: "*", Type: imap.StatusRespOk, Code: imap.CodeUidValidity, Arguments: []interface{}{uint32(2)}})
	if notices[4].Kind != IdleReconcile || notices[4].UIDValidity != 2 {
		t.Fatal("UIDVALIDITY reset lost")
	}
}

func TestIdleRenewalDisconnectCancellationAndNoPolling(t *testing.T) {
	for _, mode := range []string{"renew", "bye", "drop", "reject", "done-timeout", "no-continuation", "early-ok", "cancel", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			socket, peer := net.Pipe()
			peer.SetDeadline(time.Now().Add(3 * time.Second))
			commands := make(chan string, 32)
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				defer peer.Close()
				caps := "IMAP4rev1 IDLE"
				if mode == "unsupported" {
					caps = "IMAP4rev1"
				}
				fmt.Fprintf(peer, "* PREAUTH [CAPABILITY %s] test\r\n", caps)
				r := bufio.NewReader(peer)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					fields := strings.Fields(line)
					if len(fields) < 2 {
						return
					}
					commands <- fields[1]
					if fields[1] != "IDLE" {
						fmt.Fprintf(peer, "%s BAD unexpected\r\n", fields[0])
						continue
					}
					if mode == "drop" {
						return
					}
					if mode == "reject" {
						fmt.Fprintf(peer, "%s NO refused\r\n", fields[0])
						return
					}
					if mode == "early-ok" {
						fmt.Fprintf(peer, "%s OK idle ended\r\n", fields[0])
						return
					}
					if mode == "no-continuation" {
						_, _ = r.ReadString('\n')
						return
					}
					io.WriteString(peer, "+ idling\r\n")
					if mode == "bye" {
						io.WriteString(peer, "* BYE server closing\r\n")
						return
					}
					line, err = r.ReadString('\n')
					if err != nil {
						return
					}
					commands <- strings.TrimSpace(line)
					if mode == "done-timeout" {
						_, _ = r.ReadString('\n')
						return
					}
					fmt.Fprintf(peer, "%s OK idle complete\r\n", fields[0])
				}
			}()
			c, err := client.New(socket)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Terminate()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- watchSelected(ctx, c, 40*time.Millisecond, 30*time.Millisecond, nil) }()
			if mode == "renew" {
				for _, want := range []string{"IDLE", "DONE", "IDLE"} {
					select {
					case got := <-commands:
						if got != want {
							t.Fatalf("%s != %s", got, want)
						}
					case <-time.After(time.Second):
						t.Fatal("IDLE renewal stalled")
					}
				}
				cancel()
			} else if mode == "cancel" {
				select {
				case <-commands:
				case <-time.After(time.Second):
					t.Fatal("no IDLE")
				}
				cancel()
			}
			select {
			case err = <-done:
				if err == nil {
					t.Fatal("unexpected success")
				}
				if (mode == "cancel" || mode == "renew") && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if mode == "unsupported" && !errors.Is(err, client.ErrExtensionUnsupported) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("IDLE failed to stop")
			}
			c.Terminate()
			<-serverDone
			close(commands)
			for cmd := range commands {
				if cmd == "NOOP" {
					t.Fatal("IDLE fell back to polling")
				}
			}
		})
	}
}

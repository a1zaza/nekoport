package mail

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestSMTPDeliveryBoundaries(t *testing.T) {
	for _, mode := range []string{"success", "lost-final", "partial-reject", "save-failed"} {
		t.Run(mode, func(t *testing.T) {
			client, peer := net.Pipe()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan bool, 1)
			go func() {
				defer peer.Close()
				data := false
				defer func() { done <- data }()
				r := bufio.NewReader(peer)
				io.WriteString(peer, "220 localhost test\r\n")
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						io.WriteString(peer, "250 localhost\r\n")
					case strings.HasPrefix(line, "MAIL"):
						io.WriteString(peer, "250 ok\r\n")
					case strings.HasPrefix(line, "RCPT"):
						if mode == "partial-reject" && strings.Contains(line, "second") {
							io.WriteString(peer, "550 rejected\r\n")
						} else {
							io.WriteString(peer, "250 ok\r\n")
						}
					case strings.HasPrefix(line, "DATA"):
						data = true
						io.WriteString(peer, "354 proceed\r\n")
						for {
							l, e := r.ReadString('\n')
							if e != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
						}
						if mode == "lost-final" {
							return
						}
						io.WriteString(peer, "250 accepted\r\n")
					case strings.HasPrefix(line, "RSET"):
						io.WriteString(peer, "250 reset\r\n")
					default:
						fmt.Fprint(peer, "250 ok\r\n")
					}
				}
			}()
			c, e := smtp.NewClient(client, "localhost")
			if e != nil {
				t.Fatal(e)
			}
			phase := false
			unknown, e := deliver(c, "from@example.test", []string{"first@example.test", "second@example.test"}, []byte("Subject: test\r\n\r\nbody\r\n"), func() error {
				phase = true
				if mode == "save-failed" {
					return errors.New("disk failure")
				}
				return nil
			})
			c.Close()
			data := <-done
			if mode == "success" {
				if e != nil || unknown || !data || !phase {
					t.Fatal(unknown, e, data, phase)
				}
			} else if mode == "lost-final" {
				if e == nil || !unknown || !data || !phase {
					t.Fatal("uncertain SMTP wrongly classified", unknown, e, data, phase)
				}
			} else if e == nil || unknown || data {
				t.Fatal("DATA sent after a rejected recipient or failed durable marker", unknown, e, data)
			}
		})
	}
}

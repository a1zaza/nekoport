package mail

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/commands"
	"github.com/emersion/go-imap/responses"
)

var ErrIDLEUnsupported = errors.New("服务器不支持 IMAP IDLE；自动增量同步不可用，请手动同步或使用定时完整对齐")

// IdleEvent is a hint, never an authoritative UID or sequence-number mutation.
// In particular, EXPUNGE sequence numbers cannot safely identify local records.
type IdleEvent string

const (
	IdleCapabilities IdleEvent = "capabilities"
	IdleFlags        IdleEvent = "flags"
	IdleNewMail      IdleEvent = "new-mail"
	IdleReconcile    IdleEvent = "reconcile"
	IdleAlert        IdleEvent = "alert"
)

// WatchIMAP keeps one selected mailbox under IDLE until cancellation or failure.
// Commands use their own short timeout; IDLE has a separate renewal/deadline.
// Issue IDLE directly so the library cannot implicitly fall back to NOOP polling.
func WatchIMAP(ctx context.Context, endpoint domain.Endpoint, password, folder string, ready func(), event func(IdleNotice)) error {
	c, err := DialIMAP(ctx, endpoint, password)
	if err != nil {
		return err
	}
	defer c.Terminate()
	ok, err := c.Support("IDLE")
	if err != nil {
		return connectionError("IMAP", "IDLE 能力检查", err, endpoint.Username, password)
	}
	if !ok {
		return ErrIDLEUnsupported
	}
	selected, err := c.Select(folder, true)
	if err != nil {
		return connectionError("IMAP", "IDLE 选择文件夹", err, endpoint.Username, password)
	}
	observer := newIdleObserver(folder, event, selected)
	err = watchSelected(ctx, c, 25*time.Minute, 10*time.Second, ready, observer)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return connectionError("IMAP", "IDLE 监听", err, endpoint.Username, password)
}

type idleResponse struct {
	responses.Idle
	accepted chan struct{}
	started  bool
	observe  func(imap.Resp)
}

func (r *idleResponse) Handle(resp imap.Resp) error {
	if r.observe != nil {
		r.observe(resp)
	}
	if _, ok := resp.(*imap.ContinuationReq); ok && !r.started {
		r.started = true
		close(r.accepted)
	}
	return r.Idle.Handle(resp)
}

func watchSelected(ctx context.Context, c *client.Client, renewal, doneTimeout time.Duration, ready func(), observers ...func(imap.Resp)) error {
	if ok, err := c.Support("IDLE"); err != nil {
		return err
	} else if !ok {
		return client.ErrExtensionUnsupported
	}
	// Bound silent half-open connections; acknowledgements have shorter timers.
	c.Timeout = renewal + doneTimeout
	first := true
	for {
		stop := make(chan struct{})
		done := make(chan error, 1)
		accepted := make(chan struct{})
		go func() {
			response := &idleResponse{Idle: responses.Idle{Stop: stop, RepliesCh: make(chan []byte, 10)}, accepted: accepted}
			if len(observers) > 0 {
				response.observe = observers[0]
			}
			status, err := c.Execute(&commands.Idle{}, response)
			if err == nil {
				err = status.Err()
			}
			done <- err
		}()
		timer := time.NewTimer(doneTimeout)
		select {
		case <-accepted:
			timer.Stop()
			if first && ready != nil && ctx.Err() == nil {
				ready()
			}
			first = false
		case err := <-done:
			timer.Stop()
			close(stop)
			if err == nil {
				err = errors.New("服务器提前结束 IDLE")
			}
			return err
		case <-ctx.Done():
			timer.Stop()
			close(stop)
			c.Terminate()
			<-done
			return ctx.Err()
		case <-timer.C:
			close(stop)
			c.Terminate()
			<-done
			return errors.New("服务器未确认 IDLE，连接已关闭")
		}
		timer = time.NewTimer(renewal)
		select {
		case err := <-done:
			timer.Stop()
			close(stop)
			if err == nil {
				err = errors.New("服务器提前结束 IDLE")
			}
			return err
		case <-ctx.Done():
			timer.Stop()
			close(stop)
			c.Terminate()
			<-done
			return ctx.Err()
		case <-timer.C:
			close(stop)
		}
		// DONE must complete before the next IDLE command. A stuck server is closed.
		timer = time.NewTimer(doneTimeout)
		select {
		case err := <-done:
			timer.Stop()
			if err != nil {
				return err
			}
		case <-ctx.Done():
			timer.Stop()
			c.Terminate()
			<-done
			return ctx.Err()
		case <-timer.C:
			c.Terminate()
			<-done
			return errors.New("服务器未确认 IDLE DONE，连接已关闭")
		}
	}
}

// Immutable values are parsed on the reader thread, never copied from mutable
// client.Mailbox() pointers after delivery to another goroutine.
type IdleNotice struct {
	ReadOnly       bool
	PermanentFlags []string
	Kind           IdleEvent
	UID            uint32
	UIDValidity    uint32
	Reason         string
}

func newIdleObserver(folder string, emit func(IdleNotice), baseline ...*imap.MailboxStatus) func(imap.Resp) {
	var uidNext, validity uint32
	readOnly := true
	var permanentFlags []string
	if len(baseline) == 1 && baseline[0] != nil {
		uidNext = baseline[0].UidNext
		validity = baseline[0].UidValidity
		readOnly = baseline[0].ReadOnly
		permanentFlags = append([]string(nil), baseline[0].PermanentFlags...)
	}

	return func(resp imap.Resp) {
		n := IdleNotice{}
		switch r := resp.(type) {
		case *imap.DataResp:
			name, fields, ok := imap.ParseNamedResp(r)
			if !ok {
				return
			}
			switch name {
			case "EXISTS", "RECENT":
				n.Kind = IdleNewMail
			case "EXPUNGE":
				n.Kind = IdleReconcile
			case "FETCH":
				n.Kind = IdleFlags
				if len(fields) > 1 {
					attrs, _ := fields[1].([]interface{})
					for i := 0; i+1 < len(attrs); i += 2 {
						if strings.EqualFold(fmtField(attrs[i]), "UID") {
							u, _ := imap.ParseNumber(attrs[i+1])
							n.UID = u
						}
					}
				}
			default:
				return
			}
			n.Reason = "idle:" + strings.ToLower(name)
		case *imap.StatusResp:
			if r.Tag != "*" {
				return
			}
			number := uint32(0)
			if len(r.Arguments) > 0 {
				number, _ = imap.ParseNumber(r.Arguments[0])
			}
			switch r.Code {
			case imap.CodeUidNext:
				if number == 0 || number <= uidNext {
					return
				}
				uidNext = number
				n.Kind = IdleNewMail
			case imap.CodeUidValidity:
				if number == 0 || number == validity {
					return
				}
				validity = number
				uidNext = 0
				n.Kind = IdleReconcile
			case imap.CodeReadOnly, imap.CodeReadWrite:
				next := r.Code == imap.CodeReadOnly
				if next == readOnly {
					return
				}
				readOnly = next
				n.Kind = IdleCapabilities
			case imap.CodePermanentFlags:
				var flags []string
				if len(r.Arguments) > 0 {
					flags, _ = imap.ParseStringList(r.Arguments[0])
				}
				if strings.Join(flags, "\x00") == strings.Join(permanentFlags, "\x00") {
					return
				}
				permanentFlags = flags
				n.Kind = IdleCapabilities
			case imap.CodeAlert:
				n.Kind = IdleAlert
			default:
				if r.Type == imap.StatusRespNo || r.Type == imap.StatusRespBad {
					n.Kind = IdleAlert
				} else {
					return
				}
			}
			n.Reason = "idle:" + strings.ToLower(string(r.Code))
		default:
			return
		}
		n.UIDValidity = validity
		n.ReadOnly = readOnly
		n.PermanentFlags = append([]string(nil), permanentFlags...)
		emit(n)
	}
}
func fmtField(v interface{}) string {
	switch v := v.(type) {
	case string:
		return v
	case imap.RawString:
		return string(v)
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	}
	return ""
}

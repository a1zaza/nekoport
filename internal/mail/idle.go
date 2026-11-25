package mail

import (
	"context"
	"errors"
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
	IdleNewMail   IdleEvent = "new-mail"
	IdleReconcile IdleEvent = "reconcile"
	IdleAlert     IdleEvent = "alert"
)

func classifyIdleUpdate(u client.Update) IdleEvent {
	switch u := u.(type) {
	case *client.MailboxUpdate: // EXISTS and RECENT (do not read the client's mutable status).
		return IdleNewMail
	case *client.ExpungeUpdate, *client.MessageUpdate: // EXPUNGE and FETCH, including flags.
		return IdleReconcile
	case *client.StatusUpdate:
		if u.Status == nil {
			return ""
		}
		if u.Status.Type == imap.StatusRespNo || u.Status.Type == imap.StatusRespBad {
			return IdleAlert
		}
		switch u.Status.Code {
		case imap.CodeUidValidity, imap.CodeUidNext, imap.CodePermanentFlags, imap.CodeReadOnly, imap.CodeReadWrite:
			return IdleReconcile
		case imap.CodeAlert:
			return IdleAlert
		}
	}
	return ""
}

// WatchIMAP keeps one selected mailbox under IDLE until cancellation or failure.
// Commands use their own short timeout; IDLE has a separate renewal/deadline.
// Issue IDLE directly so the library cannot implicitly fall back to NOOP polling.
func WatchIMAP(ctx context.Context, endpoint domain.Endpoint, password, folder string, ready func(), event func(IdleEvent)) error {
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
	updates := make(chan client.Update, 64)
	c.Updates = updates
	consumed := make(chan struct{})
	go func() {
		defer close(consumed)
		for {
			select {
			case u := <-updates:
				if kind := classifyIdleUpdate(u); kind != "" && ctx.Err() == nil {
					event(kind)
				}
			case <-c.LoggedOut():
				return
			}
		}
	}()
	defer func() { c.Terminate(); <-consumed }()
	if _, err = c.Select(folder, true); err != nil {
		return connectionError("IMAP", "IDLE 选择文件夹", err, endpoint.Username, password)
	}
	err = watchSelected(ctx, c, 25*time.Minute, 10*time.Second, ready)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return connectionError("IMAP", "IDLE 监听", err, endpoint.Username, password)
}

type idleResponse struct {
	responses.Idle
	accepted chan struct{}
	started  bool
}

func (r *idleResponse) Handle(resp imap.Resp) error {
	if _, ok := resp.(*imap.ContinuationReq); ok && !r.started {
		r.started = true
		close(r.accepted)
	}
	return r.Idle.Handle(resp)
}

func watchSelected(ctx context.Context, c *client.Client, renewal, doneTimeout time.Duration, ready func()) error {
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

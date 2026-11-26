package app

import (
	"context"
	"sync"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/emersion/go-imap/client"
)

type imapUsage struct {
	total, idle int
	changed     chan struct{}
}

func imapLimit(x domain.Account) int {
	if x.IMAPMaxConnections < 4 {
		return 10
	}
	return min(x.IMAPMaxConnections, 50)
}
func (a *App) acquireIMAP(ctx context.Context, x domain.Account, idle bool) (func(), error) {
	for {
		a.mu.Lock()
		u := a.imapUsage[x.ID]
		if u == nil {
			u = &imapUsage{changed: make(chan struct{})}
			a.imapUsage[x.ID] = u
		}
		limit := imapLimit(x)
		allowed := u.total < limit
		if idle {
			idleLimit := limit - 3
			if reduced, ok := a.idleLimits[x.ID]; ok {
				idleLimit = min(idleLimit, reduced)
			}
			allowed = allowed && u.idle < idleLimit
		}
		if allowed {
			u.total++
			if idle {
				u.idle++
			}
			a.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					a.mu.Lock()
					u.total--
					if idle {
						u.idle--
					}
					close(u.changed)
					u.changed = make(chan struct{})
					a.mu.Unlock()
				})
			}, nil
		}
		changed := u.changed
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
func (a *App) dialIMAP(ctx context.Context, x domain.Account, password string) (*client.Client, error) {
	release, e := a.acquireIMAP(ctx, x, false)
	if e != nil {
		return nil, e
	}
	c, e := mailer.DialIMAP(ctx, x.IMAP, password, release)
	if e != nil {
		release()
	}
	return c, e
}
func (a *App) reduceIdle(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.imapUsage[id]
	if u == nil {
		return
	}
	limit := max(0, u.idle-1)
	if old, ok := a.idleLimits[id]; !ok || limit < old {
		a.idleLimits[id] = limit
	}
}

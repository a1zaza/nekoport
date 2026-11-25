package app

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
)

// Runtime-only connection state: no secrets, no checkpoint writes per heartbeat.
type IdleStatus struct {
	State     string    `json:"state"`
	Connected int       `json:"connected"`
	Total     int       `json:"total"`
	Error     string    `json:"error,omitempty"`
	RetryAt   time.Time `json:"retryAt,omitempty"`
	LastEvent time.Time `json:"lastEventAt,omitempty"`
}
type idleWatcher struct {
	accountID, name string
	revision        int
	cancel          context.CancelFunc
	status          IdleStatus
}
type idleDemand struct {
	full    bool
	quick   bool
	trigger string
	at      time.Time
}

// maintainIdle only examines local configuration. It performs no periodic IMAP
// queries. New folders are discovered by initial/manual/scheduled reconciliation.
func (a *App) maintainIdle(now time.Time) {
	accounts := map[string]domain.Account{}
	boxes := map[string]domain.Mailbox{}
	if a.Store.Writable() {
		a.Store.Read(func(s domain.State, _ storage.Index) {
			for id, x := range s.Accounts {
				if !x.Paused && s.SyncStatus[id].ErrorKind != "authentication" {
					accounts[id] = x
				}
			}
			for id, b := range s.Mailboxes {
				if _, ok := accounts[b.AccountID]; ok && !b.NoSelect {
					boxes[id] = b
				}
			}
		})
	}
	var deliveries []string
	a.mu.Lock()
	if a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	for id, w := range a.idleWatchers {
		b, exists := boxes[id]
		x := accounts[w.accountID]
		if !exists || w.revision != x.Revision || w.name != b.Name {
			w.cancel()
			delete(a.idleWatchers, id)
		}
	}
	for id := range a.idleAccounts {
		if _, ok := accounts[id]; !ok {
			delete(a.idleAccounts, id)
			delete(a.idlePending, id)
		}
	}
	for id, x := range accounts {
		if revision, ok := a.idleAccounts[id]; !ok || revision != x.Revision {
			deliveries = append(deliveries, id)
			a.idleAccounts[id] = x.Revision
			a.idlePending[id] = idleDemand{full: true, at: now, trigger: "startup"}
		}
	}
	// Start INBOX first; other folders also need their own selected connection.
	ids := make([]string, 0, len(boxes))
	for id := range boxes {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(l, r string) int {
		if boxes[l].Role == "inbox" && boxes[r].Role != "inbox" {
			return -1
		}
		if boxes[r].Role == "inbox" && boxes[l].Role != "inbox" {
			return 1
		}
		return 0
	})
	for _, id := range ids {
		if _, ok := a.idleWatchers[id]; ok {
			continue
		}
		b := boxes[id]
		x := accounts[b.AccountID]
		ctx, cancel := context.WithCancel(a.ctx)
		w := &idleWatcher{accountID: x.ID, name: b.Name, revision: x.Revision, cancel: cancel, status: IdleStatus{State: "connecting"}}
		a.idleWatchers[id] = w
		a.wg.Add(1)
		go func() { defer a.wg.Done(); a.watchFolder(ctx, id, w, x) }()
	}
	a.mu.Unlock()
	for _, id := range deliveries {
		a.kick(id, true)
	}
	a.flushIdle(now)
}

func (a *App) idleEvent(id string, w *idleWatcher, kind mailer.IdleEvent) {
	a.mu.Lock()
	if a.idleWatchers[id] != w || a.ctx.Err() != nil {
		a.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	w.status.LastEvent = now
	if kind == mailer.IdleAlert {
		w.status.Error = "服务器发出 IDLE ALERT，请检查邮箱服务状态"
	} else {
		d, exists := a.idlePending[w.accountID]
		d.full = d.full || kind == mailer.IdleReconcile
		d.quick = d.quick || kind == mailer.IdleNewMail
		d.trigger = "idle"
		if !exists {
			d.at = now
		}
		a.idlePending[w.accountID] = d
	}
	a.mu.Unlock()
	a.Changed()
}

// A successful local MOVE dirties both sides even when the server sends no
// unsolicited events for commands issued by this client.
func (a *App) queueMoveReconcile(accountID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		return
	}
	d, exists := a.idlePending[accountID]
	d.full = true
	if !exists {
		d.at = time.Now().UTC()
		d.trigger = "operation"
	}
	a.idlePending[accountID] = d
}

func (a *App) watchFolder(ctx context.Context, id string, w *idleWatcher, x domain.Account) {
	failures := 0
	for ctx.Err() == nil {
		_, cred, err := a.account(x.ID)
		started := time.Now()
		if err == nil {
			err = mailer.WatchIMAP(ctx, x.IMAP, cred.IMAP, w.name, func() {
				// A successful new connection invalidates network retry deadlines.
				// Keep failure counts until the compensating sync actually commits.
				_ = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
					account, exists := s.Accounts[x.ID]
					status := s.SyncStatus[x.ID]
					if !exists || account.Revision != w.revision || account.Paused || status.ErrorKind != "temporary" || status.RetryAt.IsZero() {
						return nil, nil
					}
					status.RetryAt = time.Time{}
					return []storage.Op{storage.Put("syncStatus", x.ID, status)}, nil
				})
				a.mu.Lock()
				if a.idleWatchers[id] == w {
					w.status.State = "listening"
					w.status.Error = ""
					w.status.RetryAt = time.Time{}
				}
				a.mu.Unlock()
				// A fresh selected connection cannot prove what happened offline.
				a.idleEvent(id, w, mailer.IdleReconcile)
			}, func(kind mailer.IdleEvent) { a.idleEvent(id, w, kind) })
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= time.Minute {
			failures = 0
		}
		failures++
		state := "reconnecting"
		if errors.Is(err, mailer.ErrIDLEUnsupported) {
			state = "unsupported"
		}
		if syncErrorKind(err) == "authentication" {
			state = "authentication"
		}
		if state == "reconnecting" {
			a.idleEvent(id, w, mailer.IdleReconcile)
		}
		delay := idleRetry(failures)
		a.mu.Lock()
		if a.idleWatchers[id] == w {
			w.status.State = state
			w.status.Error = cleanError(err)
			if state == "reconnecting" {
				w.status.RetryAt = time.Now().UTC().Add(delay)
			}
		}
		a.mu.Unlock()
		a.Changed()
		if state != "reconnecting" {
			<-ctx.Done()
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func idleRetry(failures int) time.Duration {
	base := time.Second * time.Duration(1<<min(max(failures-1, 0), 8))
	return base + time.Duration(rand.Int64N(int64(base/4)+1))
}

func (a *App) flushIdle(now time.Time) {
	// Keep dirty hints until no overlapping task can hide an event behind its
	// captured UID fence or deletion snapshot. Storms coalesce, never disappear.
	type request struct {
		id     string
		demand idleDemand
	}
	var requests []request
	a.mu.Lock()
	for id, d := range a.idlePending {
		if now.Sub(d.at) >= 500*time.Millisecond {
			requests = append(requests, request{id, d})
		}
	}
	a.mu.Unlock()
	for _, r := range requests {
		busy, quickBusy, blocked := false, false, false
		a.Store.Read(func(s domain.State, _ storage.Index) {
			x, ok := s.Accounts[r.id]
			st := s.SyncStatus[r.id]
			blocked = !ok || x.Paused || st.ErrorKind == "authentication" || now.Before(st.RetryAt)
			for _, task := range s.SyncTasks {
				if task.AccountID == r.id && task.Active() {
					busy = true
					quickBusy = quickBusy || task.Kind == "quick"
				}
			}
		})
		if blocked || quickBusy || busy && !r.demand.quick || !a.Store.Writable() {
			continue
		}
		// Remove before enqueue so events received during admission survive.
		a.mu.Lock()
		if current, ok := a.idlePending[r.id]; !ok || current != r.demand {
			a.mu.Unlock()
			continue
		}
		kind := "quick"
		if r.demand.full && !busy {
			kind = "reconcile"
		}
		if kind == "quick" && r.demand.full {
			remaining := r.demand
			remaining.quick = false
			a.idlePending[r.id] = remaining
		} else {
			delete(a.idlePending, r.id)
		}
		a.mu.Unlock()
		requestedAt := time.Now().UTC()
		trigger := r.demand.trigger
		if trigger == "" {
			trigger = "idle"
		}
		task, err := a.CreateSyncTask(r.id, kind, trigger, "")
		if err != nil || task.CreatedAt.Before(requestedAt) {
			a.mu.Lock()
			d, exists := a.idlePending[r.id]
			d.full = d.full || r.demand.full
			d.quick = d.quick || r.demand.quick
			if !exists {
				d.trigger = r.demand.trigger
			}
			if !exists {
				d.at = r.demand.at
			}
			a.idlePending[r.id] = d
			a.mu.Unlock()
		}
	}
}

func (a *App) IdleStatuses() map[string]IdleStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]IdleStatus{}
	for _, w := range a.idleWatchers {
		s := out[w.accountID]
		s.Total++
		if w.status.State == "listening" {
			s.Connected++
		}
		if w.status.State != "listening" {
			// Authentication and unsupported must not be masked by another folder.
			if s.State != "authentication" && s.State != "unsupported" {
				s.State = w.status.State
				s.Error = w.status.Error
			}
		}
		if !w.status.RetryAt.IsZero() && (s.RetryAt.IsZero() || w.status.RetryAt.Before(s.RetryAt)) {
			s.RetryAt = w.status.RetryAt
		}
		if w.status.LastEvent.After(s.LastEvent) {
			s.LastEvent = w.status.LastEvent
		}
		if s.Error == "" && w.status.Error != "" {
			s.Error = w.status.Error
		}
		out[w.accountID] = s
	}
	for id, s := range out {
		if s.State == "" {
			s.State = "listening"
		}
		out[id] = s
	}
	return out
}

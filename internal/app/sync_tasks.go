package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

func (a *App) CreateSyncTask(accountID, kind, trigger, requestKey string) (domain.SyncTask, error) {
	var result domain.SyncTask
	if kind != "quick" && kind != "history" && kind != "reconcile" {
		return result, errors.New("同步任务类型无效")
	}
	if len(requestKey) > 128 {
		return result, errors.New("请求标识过长")
	}
	now := time.Now().UTC()
	err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		x, ok := s.Accounts[accountID]
		if !ok {
			return nil, errors.New("邮箱不存在")
		}
		if x.Paused {
			return nil, errors.New("请先恢复此邮箱的后台同步")
		}
		var ops []storage.Op
		if kind == "history" && x.Scope() != "all" {
			x.SyncScope = "all"
			x.Revision++
			ops = append(ops, storage.Put("accounts", x.ID, x))
		}
		var quick, primary *domain.SyncTask
		for _, task := range s.SyncTasks {
			if task.AccountID != accountID {
				continue
			}
			if requestKey != "" && task.RequestKey == requestKey {
				result = task.Clone()
				return nil, nil
			}
			if task.Active() {
				copy := task.Clone()
				if task.Kind == "quick" {
					quick = &copy
				} else {
					primary = &copy
				}
			}
		}
		active := primary
		if active == nil {
			active = quick
		}
		if kind == "quick" && quick != nil {
			active = quick
		}
		// An automatic quick task may queue beside a historical task, but the account
		// worker still executes one batch at a time and always drains quick first.
		auxiliary := kind == "quick" && (trigger == "timer" || trigger == "idle") && primary != nil && quick == nil
		if active != nil && !auxiliary {
			result = active.Clone()
			if kind == "history" && result.Kind != "history" {
				result.Scope = x.Scope()
				result.RecentDays = x.SyncDays
				appendSyncLog(&result, domain.SyncLog{Action: "upgraded", Message: "任务升级为全历史同步，重新枚举"})
				result.Kind = "history"
				result.AccountRevision = x.Revision
				result.Generation = storage.ID()
				result.Folders = nil
				result.Discovered = false
				result.UpdatedAt = now
				ops = append(ops, storage.Put("syncTasks", result.ID, result))
			} else if kind == "reconcile" && result.Kind == "quick" {
				st := s.SyncStatus[accountID]
				st.AccountID = accountID
				st.PendingReconcile = true
				ops = append(ops, storage.Put("syncStatus", accountID, st))
			}
			return ops, nil
		}

		result = domain.SyncTask{ID: storage.ID(), Generation: storage.ID(), AccountID: accountID, Kind: kind, Trigger: trigger, RequestKey: requestKey, State: "queued", AccountRevision: x.Revision, CreatedAt: now, UpdatedAt: now, LogVersion: 1, Scope: x.Scope(), RecentDays: x.SyncDays}
		appendSyncLog(&result, domain.SyncLog{Action: "created", Message: "同步任务已创建"})
		st := s.SyncStatus[accountID]
		st.AccountID = accountID
		if trigger == "manual" {
			st.ErrorKind = ""
			st.RetryAt = time.Time{}
			st.Error = ""
		}
		ops = append(ops, storage.Put("syncTasks", result.ID, result), storage.Put("syncStatus", accountID, st))
		// Retain each category independently so quick jobs cannot evict full logs.

		ops = append(ops, syncRetention(s, result)...)
		return ops, nil
	})
	if err == nil && result.Active() {
		a.kick(accountID, false)
		a.Changed()
	}
	return result, err
}

func (a *App) SyncTaskAction(id, action string) error {
	var accountID string
	err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		t, ok := s.SyncTasks[id]
		if !ok {
			return nil, errors.New("同步任务不存在")
		}
		t = t.Clone()
		var ops []storage.Op
		accountID = t.AccountID
		if action == "cancel" {
			if !t.Active() {
				return nil, nil
			}
			t.State = "cancelled"
			t.FinishedAt = time.Now().UTC()
			appendSyncLog(&t, domain.SyncLog{Action: "cancelled", Message: "任务已取消，保留已提交的同步结果"})
		} else if action == "resume" {
			if t.Active() {
				return nil, nil
			}
			if t.State == "succeeded" {
				return nil, errors.New("已完成任务请重新创建")
			}
			x, ok := s.Accounts[t.AccountID]
			if !ok || x.Paused {
				return nil, errors.New("请先恢复邮箱")
			}
			for _, other := range s.SyncTasks {
				if other.AccountID == t.AccountID && other.Active() {
					return nil, errors.New("此邮箱已有同步任务")
				}
			}
			if t.Kind == "history" && x.Scope() != "all" {
				x.SyncScope = "all"
				x.Revision++
				ops = append(ops, storage.Put("accounts", x.ID, x))
			}
			if t.AccountRevision != x.Revision {
				t.Folders = nil
				t.Discovered = false
				t.Generation = storage.ID()
				t.AccountRevision = x.Revision
			}
			t.Discovered = false
			for i := range t.Folders {
				if t.Folders[i].Phase == "failed" {
					t.Folders[i].Phase = "scan"
					t.Folders[i].Error = ""
				}
			}
			t.Scope = x.Scope()
			t.RecentDays = x.SyncDays
			appendSyncLog(&t, domain.SyncLog{Action: "resumed", Message: "继续任务，统计累计已提交结果"})
			t.State = "queued"
			t.Error = ""
			t.FinishedAt = time.Time{}
		} else {
			return nil, errors.New("任务操作无效")
		}
		t.UpdatedAt = time.Now().UTC()
		st := s.SyncStatus[t.AccountID]
		st.RetryAt = time.Time{}
		st.ErrorKind = ""
		st.Error = ""
		return append(append(ops, storage.Put("syncTasks", id, t), storage.Put("syncStatus", t.AccountID, st)), syncRetention(s, t)...), nil
	})
	if err == nil {
		a.mu.Lock()
		cancel := a.taskCancels[id]
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if action == "resume" {
			a.kick(accountID, false)
		}
		a.Changed()
	}
	return err
}

func (a *App) recoverSyncTasks() {
	_ = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		ops := syncRetention(s)
		for id, t := range s.SyncTasks {
			if t.State == "running" {
				t.State = "queued"
				ops = append(ops, storage.Put("syncTasks", id, t))
			}
		}
		return ops, nil
	})
}

func (a *App) scheduleSync(now time.Time) {
	if !a.Store.Writable() {
		return
	}
	type due struct {
		id, kind, task, trigger string
	}
	var list []due
	a.Store.Read(func(s domain.State, _ storage.Index) {
		ids := make([]string, 0, len(s.Accounts))
		for id := range s.Accounts {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			x := s.Accounts[id]
			if x.Paused {
				continue
			}
			st := s.SyncStatus[id]
			if st.ErrorKind == "authentication" || now.Before(st.RetryAt) {
				continue
			}
			var active *domain.SyncTask
			for _, v := range s.SyncTasks {
				if v.AccountID == id && v.Active() && (active == nil || v.Kind == "quick") {
					copy := v
					active = &copy
				}
			}
			if active != nil {
				list = append(list, due{id: id, task: active.ID})
				continue
			}
			var retry *domain.SyncTask
			if st.Failures > 0 {
				for _, v := range s.SyncTasks {
					if v.AccountID == id && (v.State == "failed" || v.State == "partial_failed") && (retry == nil || v.UpdatedAt.After(retry.UpdatedAt)) {
						t := v
						retry = &t
					}
				}
			}
			if retry != nil {
				list = append(list, due{id: id, task: retry.ID})
				continue
			}
			if st.PendingReconcile || x.ReconcileOn() && !now.Before(st.NextFull) {
				source := "timer"
				if st.PendingReconcile {
					source = "manual"
				}
				list = append(list, due{id: id, kind: "reconcile", trigger: source})
			}
		}
	})
	// Queued work is admitted through the existing two-account worker gate.
	for _, v := range list {
		if v.task != "" {
			var state string
			a.Store.Read(func(s domain.State, _ storage.Index) { state = s.SyncTasks[v.task].State })
			if state == "failed" || state == "partial_failed" {
				_ = a.SyncTaskAction(v.task, "resume")
			}
		} else {
			source := v.trigger
			if source == "" {
				source = "timer"
			}
			_, _ = a.CreateSyncTask(v.id, v.kind, source, "")
		}
		a.kick(v.id, false)
	}
}

func (a *App) taskCommit(before, after domain.SyncTask, extra func(domain.State, storage.Index) ([]storage.Op, error)) error {
	after = after.Clone()
	after.UpdatedAt = time.Now().UTC()
	return a.Store.Update(func(s domain.State, i storage.Index) ([]storage.Op, error) {
		current, ok := s.SyncTasks[before.ID]
		x, exists := s.Accounts[before.AccountID]
		if !ok || !exists || !current.Active() || current.UpdatedAt != before.UpdatedAt || x.Revision != before.AccountRevision || x.Paused {
			return nil, storage.ErrConflict
		}
		var ops []storage.Op
		var err error
		if extra != nil {
			ops, err = extra(s, i)
			if err != nil {
				return nil, err
			}
		}
		if err := auditSyncCommit(s, i, before, &after, ops); err != nil {
			return nil, err
		}
		return append(append(ops, storage.Put("syncTasks", after.ID, after)), syncRetention(s, after)...), nil
	})
}

func (a *App) advanceSyncTask(x domain.Account, cred secrets.Credentials) bool {
	if !a.Store.Writable() {
		return false
	}
	var t domain.SyncTask
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, v := range s.SyncTasks {
			if v.AccountID == x.ID && v.Active() && (t.ID == "" || v.Kind == "quick") {
				t = v.Clone()
			}
		}
	})
	if t.ID == "" {
		return false
	}
	if t.AccountRevision != x.Revision {
		_ = a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
			v, ok := s.SyncTasks[t.ID]
			if !ok || !v.Active() {
				return nil, nil
			}
			v = v.Clone()
			v.AccountRevision = x.Revision
			v.Scope = x.Scope()
			v.RecentDays = x.SyncDays
			appendSyncLog(&v, domain.SyncLog{Action: "restarted", Message: "邮箱配置变化，重新枚举；保留累计统计"})
			v.Generation = storage.ID()
			v.Discovered = false
			v.Folders = nil
			v.State = "queued"
			v.UpdatedAt = time.Now().UTC()
			return []storage.Op{storage.Put("syncTasks", v.ID, v)}, nil
		})
		return true
	}
	ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
	defer cancel()
	a.mu.Lock()
	a.taskCancels[t.ID] = cancel
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.taskCancels, t.ID); a.mu.Unlock() }()
	c, err := mailer.DialIMAP(ctx, x.IMAP, cred.IMAP)
	if err != nil {
		a.failSync(t, -1, err)
		return false
	}
	defer c.Terminate()
	for batch := 0; batch < 8; batch++ {
		err = a.syncStep(c, x, t)
		if err != nil {
			break
		}
		var current domain.SyncTask
		a.Store.Read(func(s domain.State, _ storage.Index) { current = s.SyncTasks[t.ID].Clone() })
		if !current.Active() {
			break
		}
		t = current
	}
	if err != nil {
		if errors.Is(err, storage.ErrConflict) || errors.Is(err, context.Canceled) || ctx.Err() != nil && a.ctx.Err() != nil {
			return false
		}
		a.failSync(t, -1, err)
		return false
	}
	a.Changed()
	var active bool
	a.Store.Read(func(s domain.State, _ storage.Index) {
		for _, v := range s.SyncTasks {
			if v.AccountID == x.ID && v.Active() {
				active = true
			}
		}
	})
	return active
}
func listFolders(c *client.Client) ([]*imap.MailboxInfo, error) {
	ch := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", ch) }()
	var infos []*imap.MailboxInfo
	for info := range ch {
		infos = append(infos, info)
	}
	return infos, <-done
}
func (a *App) syncStep(c *client.Client, x domain.Account, t domain.SyncTask) error {
	before := t.Clone()
	t = t.Clone() // failed commits must not advance the caller's recovery cursor
	t.State = "running"
	if !t.Discovered {
		infos, err := listFolders(c)
		if err != nil {
			return err
		}
		t.Discovered = true
		oldFolders := append([]domain.SyncFolder(nil), t.Folders...)
		t.Folders = nil
		var boxes []domain.Mailbox
		a.Store.Read(func(s domain.State, _ storage.Index) {
			for _, info := range infos {
				var box domain.Mailbox
				for _, b := range s.Mailboxes {
					if b.AccountID == x.ID && b.Name == info.Name {
						box = b
						break
					}
				}
				if slices.Contains(info.Attributes, imap.NoSelectAttr) {
					if box.ID != "" {
						box.NoSelect = true
						boxes = append(boxes, box)
					}
					continue
				}
				box.NoSelect = false
				if box.ID == "" {
					box = domain.Mailbox{ID: storage.ID(), AccountID: x.ID, Name: info.Name, Role: role(info.Name, info.Attributes)}
				}
				boxes = append(boxes, box)
				progress := domain.SyncFolder{MailboxID: box.ID, Name: info.Name, Phase: "init"}
				for _, old := range oldFolders {
					if old.MailboxID == box.ID {
						progress = old
						if progress.Phase == "failed" {
							progress.Phase = "init"
							progress.Error = ""
						}
						break
					}
				}
				t.Folders = append(t.Folders, progress)
			}
		})
		return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
			var ops []storage.Op
			for _, b := range boxes {
				ops = append(ops, storage.Put("mailboxes", b.ID, b))
			}
			return ops, nil
		})
	}
	for idx := range t.Folders {
		f := &t.Folders[idx]
		if f.Phase == "done" || f.Phase == "failed" || f.Phase == "removed" {
			continue
		}
		status, err := c.Select(f.Name, true)
		if err != nil {
			return a.failSync(before, idx, err)
		}
		if f.UIDValidity != status.UidValidity {
			f.UIDValidity = status.UidValidity
			f.SnapshotAt = time.Time{}
			f.UpperUID = 0
			f.NextUID = 1
			f.Processed = 0
			f.Phase = "reset"
		}
		if f.Phase == "init" {
			f.NextUID = 1
			f.Phase = "reset"
		}
		if f.Phase == "reset" {
			if f.SnapshotAt.IsZero() {
				f.SnapshotAt = time.Now().UTC()
				f.RemoteTotal = status.Messages
				a.Store.Read(func(_ domain.State, i storage.Index) { f.LocalBefore = len(i.Folders[f.MailboxID]) })
			}
			upper, err := syncUpperUID(c, status)
			if err != nil {
				return a.failSync(before, idx, err)
			}
			f.UpperUID = upper
			var box domain.Mailbox
			var stale []string
			a.Store.Read(func(s domain.State, i storage.Index) {
				box = s.Mailboxes[f.MailboxID]
				for _, id := range i.Folders[f.MailboxID] {
					if s.Messages[id].UIDValidity != f.UIDValidity {
						stale = append(stale, id)
						if len(stale) == 100 {
							break
						}
					}
				}
			})
			if box.UIDValidity != f.UIDValidity {
				box.LastObservedUID = 0
			}
			box.UIDValidity = f.UIDValidity
			if len(stale) == 0 {
				f.Phase = "scan"
				if t.Kind == "quick" {
					baseline := box.LastObservedUID
					a.Store.Read(func(s domain.State, _ storage.Index) {
						for _, primary := range s.SyncTasks {
							if primary.AccountID == x.ID && primary.Kind != "quick" && primary.Active() {
								base := f.UpperUID
								for _, pf := range primary.Folders {
									if pf.MailboxID == f.MailboxID && pf.UIDValidity == f.UIDValidity {
										base = pf.UpperUID
										break
									}
								}
								baseline = max(baseline, base)
							}
						}
					})
					f.NextUID = uint64(baseline) + 1
				}
			}
			return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				ops := []storage.Op{storage.Put("mailboxes", box.ID, box)}
				for _, id := range stale {
					ops = append(ops, storage.Del("messages", id))
					ops = append(ops, conflictOperations(s, id)...)
				}
				return ops, nil
			})
		}
		if f.Phase == "scan" {
			if f.NextUID > uint64(f.UpperUID) || f.UpperUID == 0 {
				if t.Kind == "quick" {
					f.Phase = "done"
				} else {
					f.Phase = "delete"
				}
				return a.taskCommit(before, t, nil)
			}
			end := min(f.NextUID+9999, uint64(f.UpperUID))
			criteria := imap.NewSearchCriteria()
			if t.Kind == "quick" && x.Scope() == "recent" {
				criteria.Since = time.Now().AddDate(0, 0, -x.SyncDays)
			}
			criteria.Uid = new(imap.SeqSet)
			criteria.Uid.AddRange(uint32(f.NextUID), uint32(end))
			uids, err := c.UidSearch(criteria)
			if err != nil {
				return a.failSync(before, idx, err)
			}
			// Some servers implement SEARCH ranges loosely. Never accept out-of-fence UIDs.
			uids = slices.DeleteFunc(uids, func(u uint32) bool { return uint64(u) < f.NextUID || uint64(u) > end })
			slices.Sort(uids)
			complete := len(uids) <= 100
			if len(uids) > 100 {
				uids = uids[:100]
			}
			full := false
			a.Store.Read(func(s domain.State, i storage.Index) {
				for _, u := range uids {
					probe := domain.Message{AccountID: x.ID, MailboxID: f.MailboxID, UIDValidity: f.UIDValidity, UID: u}
					if _, ok := i.Remote[storage.Remote(probe)]; !ok {
						full = true
						break
					}
				}
			})
			messages, err := fetchSyncMessages(c, uids, full)
			if err != nil {
				return a.failSync(before, idx, err)
			}
			if complete {
				f.NextUID = end + 1
			} else {
				f.NextUID = uint64(uids[len(uids)-1]) + 1
			}
			f.Processed += len(messages)
			return a.taskCommit(before, t, func(s domain.State, i storage.Index) ([]storage.Op, error) {
				box := s.Mailboxes[f.MailboxID]
				var ops []storage.Op
				for _, m := range messages {
					if m.Uid == 0 || uint64(m.Uid) < before.Folders[idx].NextUID || m.Uid > f.UpperUID {
						continue
					}
					value := mailer.Envelope(m, x, box)
					oldID, exists := i.Remote[storage.Remote(value)]
					if exists {
						old := s.Messages[oldID]
						if !full {
							value = old
							value.Seen = slices.Contains(m.Flags, imap.SeenFlag)
							value.Starred = slices.Contains(m.Flags, imap.FlaggedFlag)
						}
						value.ID = oldID
						value.RawID = old.RawID
						value.ContentID = old.ContentID
						value.Preview = old.Preview
						value.CacheBytes = old.CacheBytes
						value.CachedAt = old.CachedAt
						value.Revision = old.Revision + 1
						for _, op := range s.Operations {
							if op.MessageID == oldID && (op.State == "queued" || op.State == "running") {
								if op.Kind == "seen" {
									value.Seen = old.Seen
								}
								if op.Kind == "starred" {
									value.Starred = old.Starred
								}
							}
						}
					} else if x.Scope() != "all" && m.InternalDate.Before(time.Now().AddDate(0, 0, -x.SyncDays)) {
						continue
					}
					value.LastSeenGeneration = t.Generation
					ops = append(ops, storage.Put("messages", value.ID, value))
				}
				if f.NextUID > uint64(f.UpperUID) {
					box.LastObservedUID = max(box.LastObservedUID, f.UpperUID)
					ops = append(ops, storage.Put("mailboxes", box.ID, box))
				}
				return ops, nil
			})
		}
		if f.Phase == "delete" {
			var candidates []domain.Message
			a.Store.Read(func(s domain.State, i storage.Index) {
				for _, id := range i.Folders[f.MailboxID] {
					m := s.Messages[id]
					if m.UIDValidity == f.UIDValidity && m.LastSeenGeneration != t.Generation {
						candidates = append(candidates, m)
						if len(candidates) == 100 {
							break
						}
					}
				}
			})
			present := map[uint32]bool{}
			if len(candidates) > 0 {
				criteria := imap.NewSearchCriteria()
				criteria.Uid = new(imap.SeqSet)
				for _, m := range candidates {
					criteria.Uid.AddNum(m.UID)
				}
				uids, err := c.UidSearch(criteria)
				if err != nil {
					return a.failSync(before, idx, err)
				}
				for _, u := range uids {
					present[u] = true
				}
			}
			if len(candidates) == 0 {
				f.Phase = "done"
			}
			return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				var ops []storage.Op
				for _, m := range candidates {
					v, ok := s.Messages[m.ID]
					if !ok || v.UIDValidity != f.UIDValidity {
						continue
					}
					if present[m.UID] {
						v.LastSeenGeneration = t.Generation
						ops = append(ops, storage.Put("messages", v.ID, v))
					} else {
						ops = append(ops, storage.Del("messages", v.ID))
						ops = append(ops, conflictOperations(s, v.ID)...)
					}
				}
				if f.Phase == "done" {
					box := s.Mailboxes[f.MailboxID]
					box.LastReconciledAt = time.Now().UTC()
					box.LastObservedUID = max(box.LastObservedUID, f.UpperUID)
					ops = append(ops, storage.Put("mailboxes", box.ID, box))
				}
				return ops, nil
			})
		}
	}
	// Complete LIST confirmation before pruning vanished directories. Never prune
	// a folder that failed SELECT: it is not evidence of remote deletion.
	partial := false
	for _, f := range t.Folders {
		if f.Phase == "failed" {
			partial = true
		}
	}
	if !partial && t.Kind != "quick" {
		infos, err := listFolders(c)
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, v := range infos {
			present[v.Name] = true
		}
		var gone []string
		var remove []string
		a.Store.Read(func(s domain.State, i storage.Index) {
			for id, b := range s.Mailboxes {
				if b.AccountID == x.ID && !present[b.Name] {
					gone = append(gone, id)
					for _, mid := range i.Folders[id] {
						remove = append(remove, mid)
						if len(remove) >= 100 {
							return
						}
					}
				}
			}
		})
		if len(gone) > 0 {
			return a.taskCommit(before, t, func(s domain.State, i storage.Index) ([]storage.Op, error) {
				var ops []storage.Op
				for _, id := range remove {
					ops = append(ops, storage.Del("messages", id))
					ops = append(ops, conflictOperations(s, id)...)
				}
				if len(remove) == 0 {
					for _, id := range gone {
						ops = append(ops, storage.Del("mailboxes", id))
					}
				}
				return ops, nil
			})
		}
	}
	t.State = "succeeded"
	if partial {
		t.State = "partial_failed"
		t.Error = "部分文件夹同步失败，请查看目录进度"
	}
	t.FinishedAt = time.Now().UTC()
	return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		st := s.SyncStatus[x.ID]
		st.AccountID = x.ID
		account := s.Accounts[x.ID]
		if partial {
			st.Failures++
			st.Error = t.Error
			st.ErrorKind = "temporary"
			st.RetryAt = nextRetry(st.Failures, time.Now().UTC())
		} else {
			st.Failures = 0
			st.ErrorKind = ""
			st.Error = ""
			st.RetryAt = time.Time{}
			account.Error = ""
			account.LastSync = time.Now().UTC().Format(time.RFC3339)
			if t.Kind == "quick" {
				st.LastQuick = t.FinishedAt
				st.NextQuick = time.Time{}
			} else {
				st.LastFull = t.FinishedAt
				st.NextFull = t.FinishedAt.Add(time.Duration(account.ReconcileMinutes()) * time.Minute)
				st.PendingReconcile = false
			}
		}
		return []storage.Op{storage.Put("syncStatus", x.ID, st), storage.Put("accounts", x.ID, account)}, nil
	})
}
func fetchSyncMessages(c *client.Client, uids []uint32, full bool) ([]*imap.Message, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	set := new(imap.SeqSet)
	for _, u := range uids {
		set.AddNum(u)
	}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchFlags}
	if full {
		items = append(items, imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchRFC822Size, imap.FetchBodyStructure)
	}
	ch := make(chan *imap.Message, 100)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(set, items, ch) }()
	var result []*imap.Message
	for m := range ch {
		result = append(result, m)
	}
	return result, <-done
}
func conflictOperations(s domain.State, messageID string) []storage.Op {
	var ops []storage.Op
	for id, o := range s.Operations {
		if o.MessageID == messageID && (o.State == "queued" || o.State == "running") {
			o.State = "failed"
			o.Error = "服务器邮件已删除或 UIDVALIDITY 已变化，请重新核对"
			ops = append(ops, storage.Put("operations", id, o))
		}
	}
	return ops
}
func nextRetry(n int, now time.Time) time.Time {
	minutes := []int{1, 2, 5, 15, 30, 60}
	return now.Add(time.Duration(minutes[min(max(n-1, 0), len(minutes)-1)])*time.Minute + time.Duration(now.Nanosecond()%5000)*time.Millisecond)
}
func syncErrorKind(err error) string {
	var ce *mailer.ConnectionError
	if errors.As(err, &ce) {
		if ce.Stage == "认证" && !ce.Retryable {
			return "authentication"
		}
		if !ce.Retryable {
			return "configuration"
		}
	}
	return "temporary"
}
func (a *App) failSync(t domain.SyncTask, folder int, err error) error {
	after := t.Clone()
	after.Error = cleanError(err)
	if folder >= 0 {
		after.Folders[folder].Phase = "failed"
		after.Folders[folder].Error = after.Error
		return a.taskCommit(t, after, nil)
	}
	after.State = "failed"
	after.FinishedAt = time.Now().UTC()
	return a.taskCommit(t, after, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		st := s.SyncStatus[t.AccountID]
		st.AccountID = t.AccountID
		st.Failures++
		st.Error = after.Error
		st.ErrorKind = syncErrorKind(err)
		st.RetryAt = nextRetry(st.Failures, time.Now().UTC())
		x := s.Accounts[t.AccountID]
		x.Error = after.Error
		return []storage.Op{storage.Put("syncStatus", x.ID, st), storage.Put("accounts", x.ID, x)}, nil
	})
}

func (a *App) UpdateSyncSettings(id string, revision int, scope string, days int, enabled bool, interval int) error {
	if scope != "recent" && scope != "all" || days < 1 || days > 3650 || interval < 1 || interval > 1440 {
		return errors.New("同步配置范围无效")
	}
	err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		x, ok := s.Accounts[id]
		if !ok {
			return nil, errors.New("邮箱不存在")
		}
		if x.Revision != revision {
			return nil, storage.ErrConflict
		}
		x.SyncScope = scope
		x.SyncDays = days
		x.ReconcileEnabled = &enabled
		x.ReconcileIntervalMinutes = interval
		x.Revision++
		st := s.SyncStatus[id]
		st.AccountID = id
		st.NextFull = time.Now().UTC().Add(time.Duration(interval) * time.Minute)
		if scope == "all" {
			st.NextFull = time.Time{}
		}
		ops := []storage.Op{storage.Put("accounts", id, x), storage.Put("syncStatus", id, st)}
		{
			for tid, t := range s.SyncTasks {
				if t.AccountID == id && t.Active() && ((!enabled && t.Kind == "reconcile" && t.Trigger == "timer") || (scope != "all" && t.Kind == "history")) {
					t = t.Clone()
					appendSyncLog(&t, domain.SyncLog{Action: "paused", Message: "同步设置变化，任务已暂停"})
					t.State = "paused"
					t.UpdatedAt = time.Now().UTC()
					ops = append(ops, storage.Put("syncTasks", tid, t))
				}
			}
		}
		return ops, nil
	})
	if err == nil {
		a.Changed()
	}
	return err
}

// Fetch only the last sequence number when UIDNEXT is missing. An expunge race
// that removes that sequence is retried, never interpreted as an empty folder.
func syncUpperUID(c *client.Client, status *imap.MailboxStatus) (uint32, error) {
	if status.UidNext > 0 {
		return status.UidNext - 1, nil
	}
	if status.Messages == 0 {
		return 0, nil
	}
	set := new(imap.SeqSet)
	set.AddNum(status.Messages)
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(set, []imap.FetchItem{imap.FetchUid}, ch) }()
	var upper uint32
	for m := range ch {
		upper = max(upper, m.Uid)
	}
	if err := <-done; err != nil {
		return 0, err
	}
	if upper == 0 {
		return 0, errors.New("目录在枚举期间变化，等待下次重试")
	}
	return upper, nil
}

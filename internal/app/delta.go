package app

import (
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// deltaOps is usable in the same journal transaction as MOVE/APPEND completion.
// Only unclaimed queued work is merged. Running snapshots are never extended.
func deltaOps(s domain.State, id, reason string, folders []domain.SyncFolder, discover, recovery bool, reasons ...string) []storage.Op {
	for i := range folders {
		folders[i].UIDs = append([]uint32(nil), folders[i].UIDs...)
		slices.Sort(folders[i].UIDs)
		folders[i].UIDs = slices.Compact(folders[i].UIDs)
		if len(folders[i].UIDs) > 1000 {
			folders[i].UIDs = nil
			folders[i].Flags = true
		}
	}
	if len(folders) > 1 {
		var ops []storage.Op
		for _, f := range folders {
			ops = append(ops, deltaOps(s, id, reason, []domain.SyncFolder{f}, false, false, reasons...)...)
		}
		return ops
	}
	x, ok := s.Accounts[id]
	if !ok || x.Paused {
		return nil
	}
	var t domain.SyncTask
	for _, v := range s.SyncTasks {
		same := discover && v.Discover && !v.Discovered || !discover && !v.Discover && len(folders) == 1 && len(v.Folders) == 1 && v.Folders[0].MailboxID == folders[0].MailboxID
		if same && v.AccountID == id && v.Kind == "delta" && v.State == "queued" && v.AccountRevision == x.Revision {
			t = v.Clone()
			break
		}
	}
	if t.ID == "" {
		now := time.Now().UTC()
		t = domain.SyncTask{ID: storage.ID(), Generation: storage.ID(), AccountID: id, AccountRevision: x.Revision, Kind: "delta", ScopeType: "folders", Trigger: reason, State: "queued", CreatedAt: now, UpdatedAt: now, LogVersion: 1, Scope: x.Scope(), RecentDays: x.SyncDays}
	}
	t.Discover = t.Discover || discover
	t.Recovery = t.Recovery || recovery
	for _, r := range append([]string{reason}, reasons...) {
		if !slices.Contains(t.Reasons, r) && len(t.Reasons) < 32 {
			t.Reasons = append(t.Reasons, r)
		}
	}
	for _, f := range folders {
		index := slices.IndexFunc(t.Folders, func(v domain.SyncFolder) bool { return v.MailboxID == f.MailboxID })
		if index < 0 {
			f.Phase = "init"
			t.Folders = append(t.Folders, f)
			continue
		}
		v := &t.Folders[index]
		if !t.RetryAt.IsZero() {
			v.Phase = "init"
			v.CheckedUID = 0
			v.SnapshotAt = time.Time{}
		}
		v.NewMail = v.NewMail || f.NewMail
		v.Flags = v.Flags || f.Flags
		v.Membership = v.Membership || f.Membership
		if f.ActivityFrom > 0 && (v.ActivityFrom == 0 || v.ActivityValidity != f.ActivityValidity || f.ActivityFrom < v.ActivityFrom) {
			v.ActivityFrom = f.ActivityFrom
			v.ActivityValidity = f.ActivityValidity
		}
		v.UIDs = append(v.UIDs, f.UIDs...)
		slices.Sort(v.UIDs)
		v.UIDs = slices.Compact(v.UIDs)
		if len(v.UIDs) > 1000 {
			v.UIDs = nil
			v.Flags = true
		}
	}
	t.ScopeType = "folders"
	if !t.Discover && len(t.Folders) == 1 && !t.Folders[0].NewMail && !t.Folders[0].Flags && !t.Folders[0].Membership && len(t.Folders[0].UIDs) > 0 {
		t.ScopeType = "uids"
	}
	t.UpdatedAt = time.Now().UTC()
	appendSyncLog(&t, domain.SyncLog{Action: "event", Message: "合并目录增量需求：" + reason})
	return []storage.Op{storage.Put("syncTasks", t.ID, t)}
}
func (a *App) queueDelta(id, reason string, folders []domain.SyncFolder, discover, recovery bool, reasons ...string) error {
	err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		return deltaOps(s, id, reason, folders, discover, recovery, reasons...), nil
	})
	if err == nil {
		a.kick(id, false)
		a.Changed()
	}
	return err
}

func (a *App) deltaStep(c *client.Client, x domain.Account, t domain.SyncTask) error {
	before := t.Clone()
	t = t.Clone()
	t.State = "running"
	t.Error = ""
	t.RetryAt = time.Time{}
	if t.Discover && !t.Discovered {
		return a.discoverDelta(c, x, before, t)
	}
	for index := range t.Folders {
		f := &t.Folders[index]
		if f.Phase == "done" || f.Phase == "removed" {
			continue
		}
		var box domain.Mailbox
		a.Store.Read(func(s domain.State, _ storage.Index) { box = s.Mailboxes[f.MailboxID] })
		if box.ID == "" || box.AccountID != x.ID || box.NoSelect {
			f.Phase = "removed"
			return a.taskCommit(before, t, nil)
		}
		f.Name = box.Name
		status, err := c.Select(box.Name, true)
		if err != nil {
			return err
		}
		if status.UidValidity == 0 {
			return errors.New("服务器未提供有效 UIDVALIDITY")
		}
		if f.Phase == "init" || f.UIDValidity != status.UidValidity {
			f.Initial = !box.HasBaseline()
			changed := box.UIDValidity != status.UidValidity
			if changed || f.Initial {
				t.ScopeType = "folders"
				appendSyncLog(&t, domain.SyncLog{Action: "baseline", Folder: box.Name, Message: "校验并恢复目录 UID 基线"})
			}
			if changed {
				f.Initial = true
				f.NewMail = true
				f.Flags = true
				f.Membership = true
				f.UIDs = nil
			}
			f.UIDValidity = status.UidValidity
			f.SnapshotAt = time.Now().UTC()
			f.RemoteTotal = status.Messages
			upper, e := syncUpperUID(c, status)
			if e != nil {
				return e
			}
			f.UpperUID = upper
			f.CheckedUID = 0
			f.NextUID = uint64(box.LastObservedUID) + 1
			if changed || f.Initial {
				f.NextUID = 1
			}
			f.Phase = "reset"
			return a.taskCommit(before, t, nil)
		}
		if f.Phase == "reset" {
			var stale []string
			a.Store.Read(func(s domain.State, i storage.Index) {
				for _, id := range i.Folders[box.ID] {
					if s.Messages[id].UIDValidity != f.UIDValidity {
						stale = append(stale, id)
						if len(stale) == 100 {
							break
						}
					}
				}
			})
			if len(stale) == 0 {
				f.Phase = "scan"
				if !f.NewMail && !f.Initial {
					f.Phase = "flags"
				}
			}
			return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				b := s.Mailboxes[box.ID]
				if b.UIDValidity != f.UIDValidity {
					b.LastObservedUID = 0
					b.Initialized = false
					b.BaselineVersion = 1
				}
				b.UIDValidity = f.UIDValidity
				ops := []storage.Op{storage.Put("mailboxes", b.ID, b)}
				for _, id := range stale {
					ops = append(ops, storage.Del("messages", id))
					ops = append(ops, conflictOperations(s, id)...)
				}
				return ops, nil
			})
		}
		if f.Phase == "scan" {
			if f.NextUID > uint64(f.UpperUID) || f.UpperUID == 0 {
				f.Phase = "flags"
				return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
					b := s.Mailboxes[box.ID]
					b.LastObservedUID = max(b.LastObservedUID, f.UpperUID)
					b.Initialized = true
					return []storage.Op{storage.Put("mailboxes", b.ID, b)}, nil
				})
			}
			end := min(f.NextUID+9999, uint64(f.UpperUID))
			criteria := imap.NewSearchCriteria()
			criteria.Uid = new(imap.SeqSet)
			criteria.Uid.AddRange(uint32(f.NextUID), uint32(end))
			if f.Initial && x.Scope() == "recent" {
				if f.ActivityFrom > 0 && f.ActivityFrom <= f.UpperUID && f.ActivityValidity == f.UIDValidity {
					recent := imap.NewSearchCriteria()
					recent.Since = time.Now().AddDate(0, 0, -x.SyncDays)
					activity := imap.NewSearchCriteria()
					activity.Uid = new(imap.SeqSet)
					activity.Uid.AddRange(f.ActivityFrom, f.UpperUID)
					criteria.Or = [][2]*imap.SearchCriteria{{recent, activity}}
				} else {
					criteria.Since = time.Now().AddDate(0, 0, -x.SyncDays)
				}
			}
			uids, e := c.UidSearch(criteria)
			if e != nil {
				return e
			}
			uids = slices.DeleteFunc(uids, func(u uint32) bool { return uint64(u) < f.NextUID || uint64(u) > end })
			slices.Sort(uids)
			uids = slices.Compact(uids)
			complete := len(uids) <= 100
			if !complete {
				uids = uids[:100]
			}
			messages, e := fetchSyncMessages(c, uids, true)
			if e != nil {
				return e
			}
			if complete {
				f.NextUID = end + 1
			} else {
				f.NextUID = uint64(uids[len(uids)-1]) + 1
			}
			f.Processed += len(messages)
			return a.taskCommit(before, t, func(s domain.State, i storage.Index) ([]storage.Op, error) {
				var ops []storage.Op
				for _, m := range messages {
					if !slices.Contains(uids, m.Uid) {
						continue
					}
					value := mailer.Envelope(m, x, box)
					oldID, exists := i.Remote[storage.Remote(value)]
					if !exists && f.Initial && x.Scope() == "recent" && !(f.ActivityFrom > 0 && f.ActivityValidity == f.UIDValidity && m.Uid >= f.ActivityFrom) && m.InternalDate.Before(time.Now().AddDate(0, 0, -x.SyncDays)) {
						continue
					}
					if exists {
						old := s.Messages[oldID]
						value = old
						value.Seen = slices.Contains(m.Flags, imap.SeenFlag)
						value.Starred = slices.Contains(m.Flags, imap.FlaggedFlag)
						preservePendingFlags(s, &value, old)
						value.Revision++
					}
					value.LastSeenGeneration = t.Generation
					ops = append(ops, storage.Put("messages", value.ID, value))
				}
				return ops, nil
			})
		}
		if f.Phase == "flags" {
			var candidates []domain.Message
			if f.Flags || f.Membership || len(f.UIDs) > 0 {
				a.Store.Read(func(s domain.State, i storage.Index) {
					if f.Flags || f.Membership {
						ids := i.UIDOrder[box.ID]
						start := sort.Search(len(ids), func(n int) bool { return s.Messages[ids[n]].UID > f.CheckedUID })
						for _, id := range ids[start:] {
							m := s.Messages[id]
							if m.UIDValidity == f.UIDValidity {
								candidates = append(candidates, m)
								if len(candidates) == 100 {
									break
								}
							}
						}
					} else {
						for _, uid := range f.UIDs {
							if uid <= f.CheckedUID {
								continue
							}
							id := i.Remote[storage.Remote(domain.Message{AccountID: x.ID, MailboxID: box.ID, UIDValidity: f.UIDValidity, UID: uid})]
							if id != "" {
								candidates = append(candidates, s.Messages[id])
								if len(candidates) == 100 {
									break
								}
							}
						}
					}
				})
			}
			slices.SortFunc(candidates, func(l, r domain.Message) int {
				if l.UID < r.UID {
					return -1
				}
				if l.UID > r.UID {
					return 1
				}
				return 0
			})
			if len(candidates) > 100 {
				candidates = candidates[:100]
			}
			if len(candidates) == 0 {
				f.Phase = "done"
				return a.taskCommit(before, t, nil)
			}
			uids := make([]uint32, 0, len(candidates))
			for _, m := range candidates {
				uids = append(uids, m.UID)
			}
			present := map[uint32]bool{}
			for _, u := range uids {
				present[u] = true
			}
			if f.Membership {
				found, e := searchUIDs(c, uids)
				if e != nil {
					return e
				}
				present = found
				var missing []uint32
				for _, u := range uids {
					if !found[u] {
						missing = append(missing, u)
					}
				}
				if len(missing) > 0 {
					confirmed, e := searchUIDs(c, missing)
					if e != nil {
						return e
					}
					for u := range confirmed {
						present[u] = true
					}
				}
			}
			fetch := []uint32{}
			for _, u := range uids {
				if present[u] {
					fetch = append(fetch, u)
				}
			}
			messages, e := fetchSyncMessages(c, fetch, false)
			if e != nil {
				return e
			}
			flags := map[uint32]*imap.Message{}
			for _, m := range messages {
				if slices.Contains(fetch, m.Uid) {
					flags[m.Uid] = m
				}
			}
			f.CheckedUID = candidates[len(candidates)-1].UID
			f.Processed += len(candidates)
			return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				var ops []storage.Op
				for _, m := range candidates {
					current, ok := s.Messages[m.ID]
					if !ok || current.UIDValidity != f.UIDValidity {
						continue
					}
					if !present[m.UID] && f.Membership {
						ops = append(ops, storage.Del("messages", m.ID))
						ops = append(ops, conflictOperations(s, m.ID)...)
					} else if remote := flags[m.UID]; remote != nil {
						value := current
						value.Seen = slices.Contains(remote.Flags, imap.SeenFlag)
						value.Starred = slices.Contains(remote.Flags, imap.FlaggedFlag)
						preservePendingFlags(s, &value, current)
						value.Revision++
						ops = append(ops, storage.Put("messages", value.ID, value))
					}
				}
				return ops, nil
			})
		}
	}
	t.State = "succeeded"
	t.FinishedAt = time.Now().UTC()
	return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		st := s.SyncStatus[x.ID]
		st.AccountID = x.ID
		st.LastQuick = t.FinishedAt
		st.Error = ""
		st.ErrorKind = ""
		st.Failures = 0
		st.RetryAt = time.Time{}
		account := s.Accounts[x.ID]
		account.Error = ""
		account.LastSync = t.FinishedAt.Format(time.RFC3339)
		return []storage.Op{storage.Put("syncStatus", x.ID, st), storage.Put("accounts", x.ID, account)}, nil
	})
}
func preservePendingFlags(s domain.State, value *domain.Message, old domain.Message) {
	for _, o := range s.Operations {
		if o.MessageID == old.ID && (o.State == "queued" || o.State == "running") {
			if o.Kind == "seen" {
				value.Seen = old.Seen
			}
			if o.Kind == "starred" {
				value.Starred = old.Starred
			}
		}
	}
}
func searchUIDs(c *client.Client, uids []uint32) (map[uint32]bool, error) {
	criteria := imap.NewSearchCriteria()
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddNum(uids...)
	found, e := c.UidSearch(criteria)
	present := map[uint32]bool{}
	for _, u := range found {
		if slices.Contains(uids, u) {
			present[u] = true
		}
	}
	return present, e
}

func (a *App) discoverDelta(c *client.Client, x domain.Account, before, t domain.SyncTask) error {
	infos, e := listFolders(c)
	if e != nil {
		return e
	}
	present := map[string]*imap.MailboxInfo{}
	for _, info := range infos {
		present[info.Name] = info
	}
	var ops []storage.Op
	a.Store.Read(func(s domain.State, i storage.Index) {
		for id, b := range s.Mailboxes {
			if b.AccountID != x.ID {
				continue
			}
			if _, ok := present[b.Name]; !ok {
				for _, mid := range i.Folders[id] {
					ops = append(ops, storage.Del("messages", mid))
					ops = append(ops, conflictOperations(s, mid)...)
					if len(ops) >= 100 {
						return
					}
				}
				ops = append(ops, storage.Del("mailboxes", id))
				for tid, task := range s.SyncTasks {
					if tid == t.ID || task.AccountID != x.ID || task.Kind != "delta" || !task.Active() {
						continue
					}
					task = task.Clone()
					changed := false
					remaining := false
					for i := range task.Folders {
						if task.Folders[i].MailboxID == id {
							task.Folders[i].Phase = "removed"
							changed = true
						}
						remaining = remaining || (task.Folders[i].Phase != "done" && task.Folders[i].Phase != "removed")
					}
					if changed {
						if !remaining {
							task.State = "succeeded"
							task.Error = ""
							task.RetryAt = time.Time{}
							task.FinishedAt = time.Now().UTC()
						}
						task.UpdatedAt = time.Now().UTC()
						appendSyncLog(&task, domain.SyncLog{Action: "folder-removed", Folder: b.Name, Message: "LIST 已确认目录消失，结束其读取补偿"})
						ops = append(ops, storage.Put("syncTasks", tid, task))
					}
				}

				if len(ops) >= 100 {
					return
				}
			}
		}
		for _, info := range infos {
			var b domain.Mailbox
			for _, old := range s.Mailboxes {
				if old.AccountID == x.ID && old.Name == info.Name {
					b = old
					break
				}
			}
			fresh := b.ID == ""
			if fresh && slices.Contains(info.Attributes, imap.NoSelectAttr) {
				continue
			}
			if fresh {
				b = domain.Mailbox{ID: storage.ID(), AccountID: x.ID, Name: info.Name, Role: role(info.Name, info.Attributes)}
			}
			b.NoSelect = slices.Contains(info.Attributes, imap.NoSelectAttr)
			ops = append(ops, storage.Put("mailboxes", b.ID, b))
			if !b.NoSelect && (fresh || t.Recovery) && (!slices.Contains(t.Reasons, "send:sent") || t.Recovery || b.Role == "sent") {
				if slices.IndexFunc(t.Folders, func(f domain.SyncFolder) bool { return f.MailboxID == b.ID }) < 0 {
					t.Folders = append(t.Folders, domain.SyncFolder{MailboxID: b.ID, Name: b.Name, Phase: "init", NewMail: true, Flags: true, Membership: true})
				}
			}
		}
		t.Discovered = true
	})
	if t.Discovered {
		folders := t.Folders
		t.Folders = nil
		a.Store.Read(func(s domain.State, _ storage.Index) {
			for _, f := range folders {
				ops = append(ops, deltaOps(s, x.ID, "directory-discovered", []domain.SyncFolder{f}, false, false, t.Reasons...)...)
			}
		})
	}
	return a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) { return ops, nil })
}

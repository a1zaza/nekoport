package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

const syncLogLimit = 200

func appendSyncLog(t *domain.SyncTask, event domain.SyncLog) {
	if t.LogVersion == 0 {
		return
	} // Never invent details for old tasks.
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	t.Logs = append(t.Logs, event)
	if len(t.Logs) > syncLogLimit {
		dropped := len(t.Logs) - syncLogLimit
		t.LogsDropped += dropped
		t.Logs = append([]domain.SyncLog(nil), t.Logs[dropped:]...)
	}
}

func addCounters(total *domain.SyncCounters, delta domain.SyncCounters) {
	total.Compared += delta.Compared
	total.Skipped += delta.Skipped
	total.Added += delta.Added
	total.Updated += delta.Updated
	total.Unchanged += delta.Unchanged
	total.Deleted += delta.Deleted
}

// Keep five terminal tasks in EACH category. Recovery state is never evicted.
func syncRetention(s domain.State, replacements ...domain.SyncTask) []storage.Op {
	tasks := make(map[string]domain.SyncTask, len(s.SyncTasks))
	for id, t := range s.SyncTasks {
		tasks[id] = t
	}
	for _, t := range replacements {
		tasks[t.ID] = t
	}
	groups := map[string][]domain.SyncTask{}
	for _, t := range tasks {
		if t.Active() || t.State == "paused" {
			continue
		}
		group := t.AccountID + "/full"
		if t.Kind == "quick" {
			group = t.AccountID + "/quick"
		}
		groups[group] = append(groups[group], t)
	}
	var remove []string
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			if group[i].UpdatedAt.Equal(group[j].UpdatedAt) {
				return group[i].ID > group[j].ID
			}
			return group[i].UpdatedAt.After(group[j].UpdatedAt)
		})
		for i := 5; i < len(group); i++ {
			remove = append(remove, group[i].ID)
		}
	}
	sort.Strings(remove)
	var ops []storage.Op
	for _, id := range remove {
		ops = append(ops, storage.Del("syncTasks", id))
	}
	return ops
}

// Derive audit counts from the SAME transaction as the actual metadata writes.
// A rejected journal write therefore cannot advance counts or log entries.
func auditSyncCommit(s domain.State, indexes storage.Index, before domain.SyncTask, after *domain.SyncTask, ops []storage.Op) error {
	if after.LogVersion == 0 {
		return nil
	}
	if after.StartedAt.IsZero() && after.State == "running" {
		after.StartedAt = time.Now().UTC()
	}
	type batch struct {
		stats       domain.SyncCounters
		first, last uint32
	}
	batches := map[string]*batch{}
	for _, op := range ops {
		if op.Bucket != "messages" {
			continue
		}
		old, exists := s.Messages[op.ID]
		var message domain.Message
		if op.Delete {
			if !exists {
				continue
			}
			message = old
		} else if err := json.Unmarshal(op.Value, &message); err != nil {
			return err
		}
		b := batches[message.MailboxID]
		if b == nil {
			b = &batch{}
			batches[message.MailboxID] = b
		}
		b.stats.Compared++
		if b.first == 0 || message.UID < b.first {
			b.first = message.UID
		}
		b.last = max(b.last, message.UID)
		if op.Delete {
			b.stats.Deleted++
		} else if !exists {
			b.stats.Added++
		} else {
			comparable := message
			comparable.Revision = old.Revision
			comparable.LastSeenGeneration = old.LastSeenGeneration
			if comparable.Date.Equal(old.Date) {
				comparable.Date = old.Date
			}
			if comparable.CachedAt.Equal(old.CachedAt) {
				comparable.CachedAt = old.CachedAt
			}
			if comparable == old {
				b.stats.Unchanged++
			} else {
				b.stats.Updated++
			}
		}
	}
	// FETCH can also return messages excluded by the account's import scope.
	// Persist those comparisons even though they produce no message write.
	for _, previous := range before.Folders {
		if previous.Phase != "scan" {
			continue
		}
		for _, current := range after.Folders {
			if current.MailboxID != previous.MailboxID || current.Processed <= previous.Processed {
				continue
			}
			b := batches[current.MailboxID]
			if b == nil {
				b = &batch{}
				batches[current.MailboxID] = b
			}
			skipped := current.Processed - previous.Processed - b.stats.Compared
			if skipped > 0 {
				b.stats.Skipped += skipped
				b.stats.Compared += skipped
				if b.first == 0 {
					b.first = uint32(previous.NextUID)
					b.last = uint32(min(uint64(current.UpperUID), current.NextUID-1))
				}
			}
		}
	}
	ids := make([]string, 0, len(batches))
	for id := range batches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := batches[id]
		index := -1
		for i := range after.Folders {
			if after.Folders[i].MailboxID == id {
				index = i
				break
			}
		}
		action, reason := "compare", "核对服务器邮件与本地索引"
		if index < 0 {
			box := s.Mailboxes[id]
			after.Folders = append(after.Folders, domain.SyncFolder{MailboxID: id, Name: box.Name, Phase: "removed", LocalBefore: len(indexes.Folders[id])})
			index = len(after.Folders) - 1
		}
		for _, f := range before.Folders {
			if f.MailboxID != id {
				continue
			}
			if f.UIDValidity != after.Folders[index].UIDValidity && b.stats.Deleted > 0 {
				action, reason = "uid-reset", "UIDVALIDITY 变化，失效旧 UID 记录"
				break
			}
			switch f.Phase {
			case "init", "reset":
				action, reason = "uid-reset", "UIDVALIDITY 变化，失效旧 UID 记录"
			case "delete":
				action, reason = "delete-confirm", "二次精确查询确认邮件是否仍存在"
			}
		}
		if after.Folders[index].Phase == "removed" {
			action, reason = "folder-removed", "完整 LIST 确认目录消失，清理本地记录"
		}
		addCounters(&after.Stats, b.stats)
		addCounters(&after.Folders[index].Stats, b.stats)
		appendSyncLog(after, domain.SyncLog{Action: action, Folder: after.Folders[index].Name, Message: reason, FirstUID: b.first, LastUID: b.last, Stats: b.stats})
	}
	for _, op := range ops {
		if op.Bucket != "mailboxes" {
			continue
		}
		box, exists := s.Mailboxes[op.ID]
		if op.Delete {
			appendSyncLog(after, domain.SyncLog{Action: "folder-removed", Folder: box.Name, Message: "本地目录已移除（不操作服务器）"})
		} else if !exists {
			var added domain.Mailbox
			if err := json.Unmarshal(op.Value, &added); err != nil {
				return err
			}
			appendSyncLog(after, domain.SyncLog{Action: "folder-added", Folder: added.Name, Message: "发现服务器新目录并建立本地索引"})
		}
	}
	if !before.Discovered && after.Discovered {
		appendSyncLog(after, domain.SyncLog{Action: "discovery", Message: fmt.Sprintf("目录枚举完成，共 %d 个可同步目录", len(after.Folders))})
	}
	for i, f := range after.Folders {
		if !f.SnapshotAt.IsZero() && (i >= len(before.Folders) || before.Folders[i].SnapshotAt != f.SnapshotAt) {
			appendSyncLog(after, domain.SyncLog{Action: "snapshot", Folder: f.Name, Message: fmt.Sprintf("目录快照：本地 %d 封，服务器 %d 封，本轮 UID 上界 %d", f.LocalBefore, f.RemoteTotal, f.UpperUID)})
		}
		if f.Phase == "failed" && (i >= len(before.Folders) || before.Folders[i].Phase != "failed") {
			appendSyncLog(after, domain.SyncLog{Action: "failure", Folder: f.Name, Message: f.Error})
		}
		if f.Phase == "done" && (i >= len(before.Folders) || before.Folders[i].Phase != "done") {
			message := "目录对齐完成，已核对服务器缺失 UID"
			if after.Kind == "quick" {
				message = "增量扫描完成，本次不检查远端删除"
			}
			appendSyncLog(after, domain.SyncLog{Action: "folder-done", Folder: f.Name, Message: message})
		}
	}
	if before.State != after.State && !after.Active() {
		message := "任务完成"
		if after.Error != "" {
			message = after.Error
		}
		appendSyncLog(after, domain.SyncLog{Action: after.State, Message: message})
	}
	return nil
}

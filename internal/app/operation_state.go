package app

import (
	"errors"
	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

// Only revert the failed flag intent, never a later operation or remote identity.
func rollbackOperation(s domain.State, o domain.Operation) []storage.Op {
	if !o.Optimistic || (o.Kind != "seen" && o.Kind != "starred") {
		return nil
	}
	m, ok := s.Messages[o.MessageID]
	if !ok {
		return nil
	}
	for _, newer := range s.Operations {
		if newer.ID != o.ID && newer.MessageID == o.MessageID && newer.Kind == o.Kind && !newer.CreatedAt.Before(o.CreatedAt) {
			return nil
		}
	}
	if o.Kind == "seen" {
		m.Seen = o.PreviousValue
	} else {
		m.Starred = o.PreviousValue
	}
	m.Revision++
	return []storage.Op{storage.Put("messages", m.ID, m)}
}

// ReconcileOperation only queues reads of the source/target folders. It never
// repeats a failed or uncertain MOVE, even if the source message still exists.
func (a *App) ReconcileOperation(id string) error {
	accountID := ""
	err := a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		o, exists := s.Operations[id]
		if !exists || o.State != "failed" || (o.Kind != "move" && o.Kind != "trash") {
			return nil, errors.New("只能核对失败的移动或删除操作")
		}
		x, exists := s.Accounts[o.AccountID]
		if !exists || x.Paused {
			return nil, errors.New("邮箱不存在或已暂停，请先启用邮箱")
		}
		var folders []domain.SyncFolder
		if m, exists := s.Messages[o.MessageID]; exists {
			if b, ok := s.Mailboxes[m.MailboxID]; ok && !b.NoSelect && b.AccountID == x.ID {
				folders = append(folders, domain.SyncFolder{MailboxID: b.ID, Name: b.Name, NewMail: true, Membership: true, Flags: true})
			}
		}
		if b, ok := s.Mailboxes[o.Target]; ok && !b.NoSelect && b.AccountID == x.ID {
			folders = append(folders, domain.SyncFolder{MailboxID: b.ID, Name: b.Name, NewMail: true, Membership: true, Flags: true, ActivityFrom: o.TargetUIDFloor, ActivityValidity: o.TargetUIDValidity})
		}
		if len(folders) == 0 {
			return nil, errors.New("来源和目标文件夹均已不存在，请使用邮箱完整对齐")
		}
		accountID = x.ID
		return deltaOps(s, x.ID, "operation:manual-check", folders, false, false), nil
	})
	if err == nil {
		a.kick(accountID, false)
		a.Changed()
	}
	return err
}

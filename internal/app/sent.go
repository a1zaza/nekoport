package app

import (
	"slices"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
)

func sentFolder(s domain.State, id string) domain.Mailbox {
	var boxes []domain.Mailbox
	for _, b := range s.Mailboxes {
		if b.AccountID == id && b.Role == "sent" && !b.NoSelect {
			boxes = append(boxes, b)
		}
	}
	slices.SortFunc(boxes, func(l, r domain.Mailbox) int {
		if l.Name < r.Name {
			return -1
		}
		if l.Name > r.Name {
			return 1
		}
		return 0
	})
	if len(boxes) > 0 {
		return boxes[0]
	}
	return domain.Mailbox{}
}
func (a *App) discoverSent(x domain.Account, password string) error {
	c, e := a.dialIMAP(a.ctx, x, password)
	if e != nil {
		return e
	}
	defer c.Terminate()
	infos, e := listFolders(c)
	if e != nil {
		return e
	}
	return a.Store.Update(func(s domain.State, _ storage.Index) ([]storage.Op, error) {
		current, ok := s.Accounts[x.ID]
		if !ok || current.Revision != x.Revision || current.Paused {
			return nil, storage.ErrConflict
		}
		var ops []storage.Op
		for _, info := range infos {
			if slices.Contains(info.Attributes, imap.NoSelectAttr) || role(info.Name, info.Attributes) != "sent" {
				continue
			}
			var b domain.Mailbox
			for _, old := range s.Mailboxes {
				if old.AccountID == x.ID && old.Name == info.Name {
					b = old
					break
				}
			}
			if b.ID == "" {
				b = domain.Mailbox{ID: storage.ID(), AccountID: x.ID, Name: info.Name, Role: "sent"}
			}
			b.NoSelect = false
			ops = append(ops, storage.Put("mailboxes", b.ID, b))
		}
		return ops, nil
	})
}

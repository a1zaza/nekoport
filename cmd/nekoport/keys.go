package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/secrets"
	"github.com/a1zaza/nekoport/internal/storage"
)

func openCredentialVault(store *storage.Store, keyfile string) (*secrets.Vault, error) {
	if keyfile == "" {
		keyfile = filepath.Join(store.Root(), "master.key")
	}
	if _, err := os.Lstat(keyfile); os.IsNotExist(err) {
		referenced := false
		store.Read(func(state domain.State, _ storage.Index) {
			for _, account := range state.Accounts {
				if account.CredentialRef != "" {
					referenced = true
					break
				}
			}
		})
		if referenced {
			return nil, errors.New("主密钥缺失且已有邮箱凭据引用，请恢复原 master.key，不会生成替换密钥")
		}
	}
	vaultPath := filepath.Join(store.Root(), "secrets.json")
	if err := secrets.EnsureKey(vaultPath, keyfile); err != nil {
		return nil, err
	}
	return secrets.Open(vaultPath, keyfile)
}

package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

func needsDefaultAccount(state domain.State) (bool, error) {
	settings := state.Settings
	if settings.Username != "" && settings.PasswordHash != "" {
		if _, err := bcrypt.Cost([]byte(settings.PasswordHash)); err != nil {
			return false, errors.New("现有访问账号的密码哈希无效，请恢复配置；不会自动重置")
		}
		return false, nil
	}
	if settings.Username != "" || settings.PasswordHash != "" || settings.AuthRevision != 0 {
		return false, errors.New("现有访问账号配置不完整，请恢复配置；不会自动重置")
	}
	if len(state.Accounts)+len(state.Mailboxes)+len(state.Messages)+len(state.Drafts)+len(state.Jobs)+len(state.Operations)+len(state.SyncTasks)+len(state.SyncStatus) > 0 {
		return false, errors.New("数据目录已有业务数据但缺少访问账号，请恢复原配置；不会自动创建替换账号")
	}
	return true, nil
}

// Only a new data store receives an account. The random password is never put
// into a storage operation or retained for printing on a later startup.
func bootstrapDefaultAccount(store *storage.Store, console io.Writer) error {
	var needed bool
	var err error
	store.Read(func(state domain.State, _ storage.Index) { needed, err = needsDefaultAccount(state) })
	if err != nil || !needed {
		return err
	}
	if console == nil {
		return errors.New("无法输出首次启动密码：控制台不可用")
	}
	entropy := make([]byte, 24)
	if _, err = rand.Read(entropy); err != nil {
		return err
	}
	password := base64.RawURLEncoding.EncodeToString(entropy)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	created := false
	err = store.Update(func(state domain.State, _ storage.Index) ([]storage.Op, error) {
		needed, err := needsDefaultAccount(state)
		if err != nil || !needed {
			return nil, err
		}
		settings := state.Settings
		settings.Username = "admin"
		settings.PasswordHash = string(hash)
		settings.AuthRevision = 1
		created = true
		return []storage.Op{storage.Put("settings", "", settings)}, nil
	})
	if err != nil || !created {
		return err
	}
	message := fmt.Sprintf("首次启动，已创建登录账号（初始密码仅本次显示）\n用户名：admin\n密码：%s\n请登录后在“设置与同步 → 账户安全”修改密码。\n", password)
	written, err := io.WriteString(console, message)
	if err != nil || written != len(message) {
		return errors.New("登录账号已创建，但首次密码无法输出到控制台；请检查日志输出配置，账号不会自动重置")
	}
	return nil
}

// Bind first: an occupied/invalid port must not consume the one-time password.
func listenAndBootstrap(store *storage.Store, address string, console io.Writer) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if err = bootstrapDefaultAccount(store, console); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

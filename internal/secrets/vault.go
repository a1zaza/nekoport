package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/a1zaza/nekoport/internal/storage"
)

type Credentials struct {
	IMAP string `json:"imap"`
	SMTP string `json:"smtp"`
}
type Vault struct {
	mu      sync.Mutex
	path    string
	aead    cipher.AEAD
	entries map[string]string
}

var ErrKeyExists = errors.New("密钥已存在，拒绝覆盖")

// Generate publishes a complete, synced key without replacing an existing key.
func Generate(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return ErrKeyExists
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".master-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	// Hard-link creation is atomic and fails if any file already occupies path.
	if err = os.Link(file.Name(), path); os.IsExist(err) {
		return ErrKeyExists
	} else if err != nil {
		return err
	}
	if err = os.Remove(file.Name()); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// EnsureKey only initializes a missing key when there are no saved credentials.
// A lost key must never be replaced in front of existing encrypted entries.
func EnsureKey(vaultPath, keyfile string) error {
	if _, err := os.Lstat(keyfile); err == nil {
		_, err = readKey(keyfile)
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	b, err := os.ReadFile(vaultPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		var wrapper struct {
			Version int               `json:"schemaVersion"`
			Entries map[string]string `json:"entries"`
		}
		if json.Unmarshal(b, &wrapper) != nil || wrapper.Version != 1 || wrapper.Entries == nil {
			return errors.New("主密钥缺失且凭据文件无效，请先恢复数据，不会生成替换密钥")
		}
		if len(wrapper.Entries) > 0 {
			return errors.New("主密钥缺失且已有加密凭据，请恢复原 master.key，不会生成替换密钥")
		}
	}
	if err = Generate(keyfile); errors.Is(err, ErrKeyExists) {
		_, err = readKey(keyfile)
	}
	return err
}

func readKey(keyfile string) ([]byte, error) {
	info, err := os.Stat(keyfile)
	if err != nil {
		return nil, errors.New("无法读取主密钥文件")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("主密钥必须是普通文件")
	}
	if info.Size() > 256 {
		return nil, errors.New("主密钥必须是 base64 编码的 32 字节密钥")
	}
	file, err := os.Open(keyfile)
	if err != nil {
		return nil, errors.New("无法读取主密钥文件")
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, errors.New("无法读取主密钥文件")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("主密钥必须是普通文件")
	}
	if info.Size() > 256 {
		return nil, errors.New("主密钥必须是 base64 编码的 32 字节密钥")
	}
	b, err := io.ReadAll(io.LimitReader(file, 256))
	if err != nil {
		return nil, errors.New("无法读取主密钥文件")
	}
	key, err := base64.StdEncoding.DecodeString(stringTrim(b))
	if err != nil || len(key) != 32 {
		return nil, errors.New("主密钥必须是 base64 编码的 32 字节密钥")
	}
	return key, nil
}

func Open(path, keyfile string) (*Vault, error) {
	key, e := readKey(keyfile)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	a, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	v := &Vault{path: path, aead: a, entries: map[string]string{}}
	b, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if e == nil {
		var wrapper struct {
			Version int               `json:"schemaVersion"`
			Entries map[string]string `json:"entries"`
		}
		if json.Unmarshal(b, &wrapper) != nil || wrapper.Version != 1 || wrapper.Entries == nil {
			return nil, errors.New("凭据文件格式错误")
		}
		v.entries = wrapper.Entries
		for id := range v.entries {
			if _, e = v.get(id); e != nil {
				return nil, errors.New("主密钥不匹配或凭据文件损坏")
			}
		}
	}
	return v, nil
}
func stringTrim(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}
func (v *Vault) Put(c Credentials) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	id := storage.ID()
	n := make([]byte, v.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		return "", e
	}
	plain, _ := json.Marshal(c)
	ciphertext := v.aead.Seal(n, n, plain, []byte(id))
	v.entries[id] = base64.StdEncoding.EncodeToString(ciphertext)
	b, e := json.MarshalIndent(struct {
		Version int               `json:"schemaVersion"`
		Entries map[string]string `json:"entries"`
	}{1, v.entries}, "", "  ")
	if e == nil {
		e = storage.WriteFile(v.path, b)
	}
	if e != nil {
		delete(v.entries, id)
		return "", e
	}
	return id, nil
}
func (v *Vault) Get(id string) (Credentials, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.get(id)
}
func (v *Vault) get(id string) (Credentials, error) {
	var c Credentials
	b, e := base64.StdEncoding.DecodeString(v.entries[id])
	if e != nil || len(b) < v.aead.NonceSize() {
		return c, errors.New("凭据不存在或损坏")
	}
	n := b[:v.aead.NonceSize()]
	plain, e := v.aead.Open(nil, n, b[v.aead.NonceSize():], []byte(id))
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(plain, &c)
	return c, e
}

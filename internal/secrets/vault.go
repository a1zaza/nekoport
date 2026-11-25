package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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

func Generate(path string) error {
	if _, e := os.Stat(path); e == nil {
		return errors.New("密钥已存在，拒绝覆盖")
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write([]byte(base64.StdEncoding.EncodeToString(b) + "\n")); e != nil {
		return e
	}
	return f.Sync()
}
func Open(path, keyfile string) (*Vault, error) {
	b, e := os.ReadFile(keyfile)
	if e != nil {
		return nil, errors.New("无法读取主密钥文件")
	}
	key, e := base64.StdEncoding.DecodeString(stringTrim(b))
	if e != nil || len(key) != 32 {
		return nil, errors.New("主密钥必须是 base64 编码的 32 字节密钥")
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
	b, e = os.ReadFile(path)
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

// Package storage implements a single-writer text store. Only a synced JSONL
// record commits a transaction; checkpoints and indexes can be reconstructed.
package storage

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	"golang.org/x/sys/unix"
)

const maxLine = 8 << 20
const MaxMessages = 200000

var ErrConflict = errors.New("内容已更新，请刷新后重试")

type Op struct {
	Bucket string          `json:"bucket"`
	ID     string          `json:"id"`
	Delete bool            `json:"delete,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
}

func Put(bucket, id string, v any) Op {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return Op{Bucket: bucket, ID: id, Value: b}
}
func Del(bucket, id string) Op { return Op{Bucket: bucket, ID: id, Delete: true} }

type payload struct {
	Version int    `json:"schemaVersion"`
	Seq     string `json:"seq"`
	ID      string `json:"txId"`
	Ops     []Op   `json:"ops"`
}
type record struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}
type manifest struct {
	Version int               `json:"schemaVersion"`
	Seq     string            `json:"lastSeq"`
	Files   map[string]string `json:"files"`
}
type commit struct {
	Manifest string `json:"manifest"`
	Hash     string `json:"hash"`
	Seq      string `json:"lastSeq"`
}
type Index struct {
	UIDOrder map[string][]string
	Remote   map[string]string
	Folders  map[string][]string
	All      []string
	Unread   map[string]int
	Keys     map[string]string
}
type Store struct {
	mu              sync.RWMutex
	root            string
	lock            *os.File
	log             *os.File
	state           domain.State
	seq             uint64
	index           Index
	failed          bool
	closed          bool
	sinceCheckpoint int
}

func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Remote(m domain.Message) string {
	return fmt.Sprintf("%s/%s/%d/%d", m.AccountID, m.MailboxID, m.UIDValidity, m.UID)
}
func Open(root string) (*Store, error) {
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	root, _ = filepath.Abs(root)
	if e := syncDir(filepath.Dir(root)); e != nil {
		return nil, e
	}
	l, e := os.OpenFile(filepath.Join(root, "writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = unix.Flock(int(l.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		l.Close()
		return nil, errors.New("数据目录已被其他进程占用")
	}
	s := &Store{root: root, lock: l, state: domain.Empty()}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	for _, d := range []string{"journal", "checkpoints", "objects", "tmp", "recovery"} {
		if e = os.MkdirAll(filepath.Join(root, d), 0700); e != nil {
			return nil, e
		}
	}
	meta := filepath.Join(root, "store.json")
	if b, err := os.ReadFile(meta); err == nil {
		var v struct {
			Version int `json:"schemaVersion"`
		}
		if json.Unmarshal(b, &v) != nil || v.Version != 1 {
			return nil, errors.New("数据目录格式不受支持")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		if e = WriteFile(meta, []byte("{\"schemaVersion\":1,\"storeId\":\""+ID()+"\"}\n")); e != nil {
			return nil, e
		}
	}
	if e = s.recover(); e != nil {
		return nil, e
	}
	if s.state.SyncTasks == nil {
		s.state.SyncTasks = map[string]domain.SyncTask{}
	}
	if s.state.SyncStatus == nil {
		s.state.SyncStatus = map[string]domain.SyncStatus{}
	}
	s.reindex()
	p := filepath.Join(root, "journal", fmt.Sprintf("%020d.jsonl", s.seq+1))
	s.log, e = os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	if e = syncDir(filepath.Dir(p)); e != nil {
		return nil, e
	}
	ok = true
	return s, nil
}
func (s *Store) Root() string { return s.root }
func (s *Store) Read(fn func(domain.State, Index)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.state, s.index)
}
func (s *Store) Update(fn func(domain.State, Index) ([]Op, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed || s.closed {
		return errors.New("存储不可写，请检查磁盘并重启恢复")
	}
	ops, e := fn(s.state, s.index)
	if e != nil {
		return e
	}
	if len(ops) == 0 {
		return nil
	}
	changedIDs := map[string]bool{}
	count := len(s.state.Messages)
	for _, op := range ops {
		if op.Bucket == "messages" {
			exists, seen := changedIDs[op.ID]
			if !seen {
				_, exists = s.state.Messages[op.ID]
			}
			if op.Delete && exists {
				count--
				exists = false
			} else if !op.Delete && !exists {
				count++
				exists = true
			}
			changedIDs[op.ID] = exists
		}
	}
	if count > MaxMessages {
		return errors.New("本地邮件索引容量已达 20 万封，请清理不再使用的本地邮箱后重试")
	}
	// Decode every operation before commit so malformed input cannot poison recovery.
	for _, op := range ops {
		if e = validate(op); e != nil {
			return e
		}
	}
	b, e := json.Marshal(payload{1, strconv.FormatUint(s.seq+1, 10), ID(), ops})
	if e != nil {
		return e
	}
	line, _ := json.Marshal(record{b, digest(b)})
	if len(line) > maxLine {
		return errors.New("事务过大，请分批保存")
	}
	line = append(line, '\n')
	if _, e = s.log.Write(line); e == nil {
		e = s.log.Sync()
	}
	if e != nil {
		s.failed = true
		return fmt.Errorf("保存结果待确认，已停止写入: %w", e)
	}
	previous := map[string]domain.Message{}
	for _, op := range ops {
		if op.Bucket == "messages" {
			previous[op.ID] = s.state.Messages[op.ID]
		}
	}
	for _, op := range ops {
		if e = apply(&s.state, op); e != nil {
			s.failed = true
			return e
		}
	}
	s.seq++
	s.updateIndex(previous, ops)
	s.sinceCheckpoint++
	info, _ := s.log.Stat()
	if s.sinceCheckpoint >= 1000 || info != nil && info.Size() >= 16<<20 {
		if e = s.checkpoint(); e != nil {
			return nil
		}
	} // journal is already durable; checkpoint failure cannot undo success
	return nil
}
func validate(op Op) error {
	if op.ID == "" && op.Bucket != "settings" {
		return errors.New("缺少对象 ID")
	}
	t := domain.Empty()
	return apply(&t, op)
}
func set[T any](m map[string]T, op Op) error {
	if op.Delete {
		delete(m, op.ID)
		return nil
	}
	var v T
	if e := json.Unmarshal(op.Value, &v); e != nil {
		return e
	}
	m[op.ID] = v
	return nil
}
func apply(s *domain.State, op Op) error {
	switch op.Bucket {
	case "accounts":
		return set(s.Accounts, op)
	case "mailboxes":
		return set(s.Mailboxes, op)
	case "messages":
		return set(s.Messages, op)
	case "drafts":
		return set(s.Drafts, op)
	case "jobs":
		return set(s.Jobs, op)
	case "syncTasks":
		return set(s.SyncTasks, op)
	case "syncStatus":
		return set(s.SyncStatus, op)
	case "operations":
		return set(s.Operations, op)
	case "settings":
		return json.Unmarshal(op.Value, &s.Settings)
	default:
		return errors.New("未知存储集合")
	}
}
func (s *Store) reindex() {
	i := Index{UIDOrder: map[string][]string{}, Remote: map[string]string{}, Folders: map[string][]string{}, Unread: map[string]int{}, Keys: map[string]string{}}
	for id, m := range s.state.Messages {
		i.Remote[Remote(m)] = id
		i.Folders[m.MailboxID] = append(i.Folders[m.MailboxID], id)
		i.All = append(i.All, id)
		if !m.Seen {
			i.Unread[m.MailboxID]++
		}
	}
	less := func(a, b string) bool {
		ma, mb := s.state.Messages[a], s.state.Messages[b]
		if ma.Date.Equal(mb.Date) {
			return a > b
		}
		return ma.Date.After(mb.Date)
	}
	sort.Slice(i.All, func(a, b int) bool { return less(i.All[a], i.All[b]) })
	for k, ids := range i.Folders {
		sort.Slice(ids, func(a, b int) bool { return less(ids[a], ids[b]) })
		i.Folders[k] = ids
		ordered := append([]string(nil), ids...)
		sort.Slice(ordered, func(a, b int) bool {
			x, y := s.state.Messages[ordered[a]], s.state.Messages[ordered[b]]
			if x.UID == y.UID {
				return x.ID < y.ID
			}
			return x.UID < y.UID
		})
		i.UIDOrder[k] = ordered
	}
	for id, j := range s.state.Jobs {
		i.Keys[j.Key] = id
	}
	s.index = i
}
func (s *Store) recover() error {
	commits, _ := filepath.Glob(filepath.Join(s.root, "checkpoints", "committed-*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(commits)))
	var minimumSeq uint64
	for _, path := range commits {
		var n uint64
		fmt.Sscanf(filepath.Base(path), "committed-%d-", &n)
		if n > minimumSeq {
			minimumSeq = n
		}
	}
	var lastErr error
	for _, p := range commits {
		b, e := os.ReadFile(p)
		var c commit
		if e != nil || json.Unmarshal(b, &c) != nil {
			lastErr = errors.New("检查点提交记录损坏")
			continue
		}
		mp := filepath.Join(s.root, "checkpoints", c.Manifest)
		if !safeRelative(c.Manifest) {
			return errors.New("检查点路径非法")
		}
		mb, e := os.ReadFile(mp)
		if e != nil || digest(mb) != c.Hash {
			lastErr = errors.New("检查点 manifest 损坏")
			continue
		}
		var m manifest
		if json.Unmarshal(mb, &m) != nil || m.Version != 1 {
			return errors.New("检查点版本不受支持")
		}
		seq, e := strconv.ParseUint(m.Seq, 10, 64)
		if e != nil || c.Seq != m.Seq {
			return errors.New("检查点序号错误")
		}
		st := domain.Empty()
		good := true
		for name, hash := range m.Files {
			if !safeRelative(name) {
				return errors.New("检查点文件路径非法")
			}
			data, e := os.ReadFile(filepath.Join(filepath.Dir(mp), name))
			if e != nil || digest(data) != hash {
				good = false
				break
			}
			if name == "state.json" {
				if json.Unmarshal(data, &st) != nil {
					good = false
					break
				}
			} else {
				sc := bufio.NewScanner(strings.NewReader(string(data)))
				sc.Buffer(make([]byte, 4096), maxLine)
				for sc.Scan() {
					var msg domain.Message
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						good = false
						break
					}
					if len(st.Messages) >= MaxMessages {
						if _, ok := st.Messages[msg.ID]; !ok {
							return errors.New("检查点邮件索引容量超过 20 万封")
						}
					}
					st.Messages[msg.ID] = msg
				}
				if sc.Err() != nil {
					good = false
				}
			}
		}
		if !good {
			lastErr = errors.New("检查点分片损坏")
			continue
		}
		if st.SyncTasks == nil {
			st.SyncTasks = map[string]domain.SyncTask{}
		}
		if st.SyncStatus == nil {
			st.SyncStatus = map[string]domain.SyncStatus{}
		}
		s.state = st
		s.seq = seq
		lastErr = nil
		break
	}
	if lastErr != nil {
		return lastErr
	}
	checkpointSeq := s.seq
	paths, _ := filepath.Glob(filepath.Join(s.root, "journal", "*.jsonl"))
	sort.Strings(paths)
	for n, p := range paths {
		start, err := strconv.ParseUint(strings.TrimSuffix(filepath.Base(p), ".jsonl"), 10, 64)
		if err != nil {
			return errors.New("日志段名称错误")
		}
		if start > 0 && start-1 > minimumSeq {
			minimumSeq = start - 1
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		r := bufio.NewReaderSize(f, 64<<10)
		var offset int64
		for {
			line, err := readLine(r)
			if len(line) > maxLine {
				f.Close()
				return errors.New("日志行超过限制")
			}
			if len(line) == 0 && err == io.EOF {
				break
			}
			if err == io.EOF {
				if n != len(paths)-1 {
					f.Close()
					return errors.New("非活动日志存在不完整尾记录")
				}
				tail := append([]byte(nil), line...)
				if e = WriteFile(filepath.Join(s.root, "recovery", ID()+".tail"), tail); e != nil {
					f.Close()
					return e
				}
				if e = os.Truncate(p, offset); e != nil {
					f.Close()
					return e
				}
				tf, te := os.OpenFile(p, os.O_WRONLY, 0600)
				if te != nil {
					f.Close()
					return te
				}
				te = tf.Sync()
				tf.Close()
				if te != nil {
					f.Close()
					return te
				}
				break
			}
			if err != nil {
				f.Close()
				return err
			}
			offset += int64(len(line))
			var rec record
			var pay payload
			if json.Unmarshal(line, &rec) != nil || digest(rec.Payload) != rec.Checksum || json.Unmarshal(rec.Payload, &pay) != nil || pay.Version != 1 {
				f.Close()
				return fmt.Errorf("日志损坏: %s:%d", filepath.Base(p), offset)
			}
			seq, e := strconv.ParseUint(pay.Seq, 10, 64)
			if e != nil {
				f.Close()
				return e
			}
			if seq <= checkpointSeq {
				continue
			}
			if seq != s.seq+1 {
				f.Close()
				return errors.New("日志序号不连续，禁止写入")
			}
			for _, op := range pay.Ops {
				if e = apply(&s.state, op); e != nil {
					f.Close()
					return e
				}
			}
			if len(s.state.Messages) > MaxMessages {
				f.Close()
				return errors.New("日志邮件索引容量超过 20 万封")
			}
			s.seq = seq
		}
		f.Close()
	}
	if s.seq < minimumSeq {
		return errors.New("恢复链不完整：缺少已提交序号，禁止降级丢失数据")
	}
	return nil
}
func safeRelative(p string) bool   { return !filepath.IsAbs(p) && p != "." && !strings.Contains(p, "..") }
func (s *Store) Checkpoint() error { s.mu.Lock(); defer s.mu.Unlock(); return s.checkpoint() }
func (s *Store) checkpoint() error {
	if s.closed || s.failed {
		return errors.New("存储不可写，无法生成检查点")
	}
	gen := fmt.Sprintf("g-%020d-%s", s.seq, ID())
	dir := filepath.Join(s.root, "checkpoints", gen)
	if e := os.Mkdir(dir, 0700); e != nil {
		return e
	}
	m := manifest{1, strconv.FormatUint(s.seq, 10), map[string]string{}}
	st := s.state
	st.Messages = map[string]domain.Message{}
	b, e := json.MarshalIndent(st, "", "  ")
	if e != nil {
		return e
	}
	if e = WriteFile(filepath.Join(dir, "state.json"), b); e != nil {
		return e
	}
	m.Files["state.json"] = digest(b)
	ids := s.index.All
	for k := 0; k < len(ids); k += 1000 {
		end := min(k+1000, len(ids))
		var buf strings.Builder
		for _, id := range ids[k:end] {
			v, _ := json.Marshal(s.state.Messages[id])
			buf.Write(v)
			buf.WriteByte('\n')
		}
		name := fmt.Sprintf("messages-%06d.jsonl", k/1000)
		data := []byte(buf.String())
		if e = WriteFile(filepath.Join(dir, name), data); e != nil {
			return e
		}
		m.Files[name] = digest(data)
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if e = WriteFile(filepath.Join(dir, "manifest.json"), mb); e != nil {
		return e
	}
	cb, _ := json.Marshal(commit{gen + "/manifest.json", digest(mb), m.Seq})
	if e = WriteFile(filepath.Join(s.root, "checkpoints", fmt.Sprintf("committed-%020d-%s.json", s.seq, gen)), cb); e != nil {
		return e
	}
	// Rotate only after publishing a complete checkpoint. Old logs remain a recovery chain.
	next, e := os.OpenFile(filepath.Join(s.root, "journal", fmt.Sprintf("%020d.jsonl", s.seq+1)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if e = syncDir(filepath.Join(s.root, "journal")); e != nil {
		next.Close()
		return e
	}
	s.log.Close()
	s.log = next
	s.sinceCheckpoint = 0
	s.prune()
	return nil
}
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var e error
	if s.log != nil {
		e = s.log.Sync()
		s.log.Close()
	}
	if s.lock != nil {
		unix.Flock(int(s.lock.Fd()), unix.LOCK_UN)
		s.lock.Close()
	}
	return e
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func WriteFile(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return syncDir(filepath.Dir(path))
}
func (s *Store) Object(b []byte) (string, error) { id := ID(); return id, s.PutObject(id, b) }
func (s *Store) PutObject(id string, b []byte) error {
	if len(id) != 32 {
		return errors.New("对象 ID 非法")
	}
	if _, e := hex.DecodeString(id); e != nil {
		return e
	}
	path := filepath.Join(s.root, "objects", id[:2], id)
	if _, e := os.Stat(path); e == nil {
		return errors.New("不可变对象已存在")
	}
	if e := WriteFile(path, b); e != nil {
		return e
	}
	meta, _ := json.Marshal(struct {
		Size int    `json:"size"`
		Hash string `json:"sha256"`
	}{len(b), digest(b)})
	if e := WriteFile(path+".json", meta); e != nil {
		return e
	}
	return syncDir(filepath.Join(s.root, "objects"))
}
func (s *Store) LoadObject(id string) ([]byte, error) {
	if len(id) != 32 {
		return nil, errors.New("对象 ID 非法")
	}
	if _, e := hex.DecodeString(id); e != nil {
		return nil, e
	}
	path := filepath.Join(s.root, "objects", id[:2], id)
	meta, e := os.ReadFile(path + ".json")
	if e != nil {
		return nil, e
	}
	var desc struct {
		Size int    `json:"size"`
		Hash string `json:"sha256"`
	}
	if json.Unmarshal(meta, &desc) != nil || desc.Size < 0 || desc.Size > 32<<20 {
		return nil, errors.New("对象描述损坏")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, int64(desc.Size)+1))
	if e != nil {
		return nil, e
	}
	if len(b) != desc.Size || digest(b) != desc.Hash {
		return nil, errors.New("对象内容校验失败")
	}
	return b, nil
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, e := r.ReadSlice('\n')
		if len(line)+len(part) > maxLine {
			return nil, errors.New("日志行超过限制")
		}
		line = append(line, part...)
		if e != bufio.ErrBufferFull {
			return line, e
		}
	}
}
func (s *Store) updateIndex(old map[string]domain.Message, ops []Op) {
	reorder := false
	folders := map[string]bool{}
	changed := map[string]bool{}
	for id, prev := range old {
		next := s.state.Messages[id]
		changed[id] = true
		if prev.ID != "" {
			delete(s.index.Remote, Remote(prev))
			if !prev.Seen {
				s.index.Unread[prev.MailboxID]--
			}
		}
		if next.ID != "" {
			s.index.Remote[Remote(next)] = id
			if !next.Seen {
				s.index.Unread[next.MailboxID]++
			}
		}
		if prev.ID == "" || next.ID == "" || prev.MailboxID != next.MailboxID || prev.UID != next.UID || prev.UIDValidity != next.UIDValidity || !prev.Date.Equal(next.Date) {
			reorder = true
			folders[prev.MailboxID] = true
			folders[next.MailboxID] = true
		}
	}
	less := func(a, b string) bool {
		x, y := s.state.Messages[a], s.state.Messages[b]
		if x.Date.Equal(y.Date) {
			return a > b
		}
		return x.Date.After(y.Date)
	}
	if reorder {
		all := make([]string, 0, len(s.state.Messages))
		for _, id := range s.index.All {
			if !changed[id] {
				all = append(all, id)
			}
		}
		additions := make([]string, 0, len(changed))
		for id := range changed {
			if s.state.Messages[id].ID != "" {
				additions = append(additions, id)
			}
		}
		sort.Slice(additions, func(i, j int) bool { return less(additions[i], additions[j]) })
		// Existing unchanged IDs retain their order. Merge the small changed
		// batch rather than sorting the entire historical index on every write.
		merged := make([]string, 0, len(all)+len(additions))
		left, right := 0, 0
		for left < len(all) && right < len(additions) {
			if less(additions[right], all[left]) {
				merged = append(merged, additions[right])
				right++
			} else {
				merged = append(merged, all[left])
				left++
			}
		}
		merged = append(merged, all[left:]...)
		merged = append(merged, additions[right:]...)
		all = merged
		s.index.All = all
		for folder := range folders {
			if folder == "" {
				continue
			}
			ids := []string{}
			for _, id := range all {
				if s.state.Messages[id].MailboxID == folder {
					ids = append(ids, id)
				}
			}
			s.index.Folders[folder] = ids
			retained := make([]string, 0, len(s.index.UIDOrder[folder]))
			for _, id := range s.index.UIDOrder[folder] {
				if !changed[id] {
					retained = append(retained, id)
				}
			}
			var additions []string
			for id := range changed {
				if m := s.state.Messages[id]; m.ID != "" && m.MailboxID == folder {
					additions = append(additions, id)
				}
			}
			uidLess := func(a, b string) bool {
				x, y := s.state.Messages[a], s.state.Messages[b]
				if x.UID == y.UID {
					return a < b
				}
				return x.UID < y.UID
			}
			sort.Slice(additions, func(a, b int) bool { return uidLess(additions[a], additions[b]) })
			ordered := make([]string, 0, len(retained)+len(additions))
			left, right := 0, 0
			for left < len(retained) && right < len(additions) {
				if uidLess(additions[right], retained[left]) {
					ordered = append(ordered, additions[right])
					right++
				} else {
					ordered = append(ordered, retained[left])
					left++
				}
			}
			ordered = append(ordered, retained[left:]...)
			ordered = append(ordered, additions[right:]...)
			s.index.UIDOrder[folder] = ordered
		}
	}
	for _, op := range ops {
		if op.Bucket == "jobs" {
			if j, ok := s.state.Jobs[op.ID]; ok {
				s.index.Keys[j.Key] = j.ID
			} else {
				for key, id := range s.index.Keys {
					if id == op.ID {
						delete(s.index.Keys, key)
					}
				}
			}
		}
	}
}
func (s *Store) prune() {
	files, _ := filepath.Glob(filepath.Join(s.root, "checkpoints", "committed-*.json"))
	sort.Strings(files)
	if len(files) < 2 {
		return
	}
	var older commit
	b, e := os.ReadFile(files[len(files)-2])
	if e != nil || json.Unmarshal(b, &older) != nil {
		return
	}
	covered, e := strconv.ParseUint(older.Seq, 10, 64)
	if e != nil {
		return
	}
	for _, p := range files[:len(files)-2] {
		b, e := os.ReadFile(p)
		var c commit
		if e != nil || json.Unmarshal(b, &c) != nil || !safeRelative(c.Manifest) {
			continue
		}
		os.Remove(p)
		os.RemoveAll(filepath.Dir(filepath.Join(s.root, "checkpoints", c.Manifest)))
	}
	logs, _ := filepath.Glob(filepath.Join(s.root, "journal", "*.jsonl"))
	sort.Strings(logs)
	for i := 0; i+1 < len(logs); i++ {
		start, e := strconv.ParseUint(strings.TrimSuffix(filepath.Base(logs[i+1]), ".jsonl"), 10, 64)
		if e == nil && start > 0 && start-1 <= covered && logs[i] != s.log.Name() {
			os.Remove(logs[i])
		}
	}
}

// Collect removes only old unreachable objects, conservatively retaining IDs
// from both checkpoint generations and their complete recovery journal chain.
func (s *Store) Collect() error {
	s.mu.Lock()
	if s.failed || s.closed {
		s.mu.Unlock()
		return errors.New("存储不可写")
	}
	initialSeq := s.seq
	b, _ := json.Marshal(s.state)
	s.mu.Unlock()
	roots := map[string]bool{}
	var mark func(any)
	mark = func(v any) {
		switch x := v.(type) {
		case string:
			if len(x) == 32 {
				if _, e := hex.DecodeString(x); e == nil {
					roots[x] = true
				}
			}
		case []any:
			for _, v := range x {
				mark(v)
			}
		case map[string]any:
			for _, v := range x {
				mark(v)
			}
		}
	}
	parse := func(b []byte) {
		var v any
		if json.Unmarshal(b, &v) == nil {
			mark(v)
		}
	}
	parse(b)
	files, _ := filepath.Glob(filepath.Join(s.root, "checkpoints", "g-*", "*.json*"))
	for _, p := range files {
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		if strings.HasSuffix(p, ".jsonl") {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 4096), maxLine)
			for sc.Scan() {
				parse(sc.Bytes())
			}
			e = sc.Err()
		} else {
			b, err := io.ReadAll(io.LimitReader(f, 32<<20))
			e = err
			parse(b)
		}
		f.Close()
		if e != nil {
			return e
		}
	}
	logs, _ := filepath.Glob(filepath.Join(s.root, "journal", "*.jsonl"))
	for _, p := range logs {
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4096), maxLine)
		for sc.Scan() {
			parse(sc.Bytes())
		}
		e = sc.Err()
		f.Close()
		if e != nil {
			return e
		}
	}
	visited := map[string]bool{}
	pending := make([]string, 0, len(roots))
	for id := range roots {
		pending = append(pending, id)
	}
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if visited[id] || len(id) != 32 {
			continue
		}
		visited[id] = true
		path := filepath.Join(s.root, "objects", id[:2], id)
		f, e := os.Open(path)
		if e != nil {
			continue
		}
		info, e := f.Stat()
		if e != nil || info.Size() > 32<<20 {
			f.Close()
			continue
		}
		b, e := io.ReadAll(f)
		f.Close()
		if e != nil {
			continue
		}
		before := len(roots)
		parse(b)
		if len(roots) > before {
			var c domain.Content
			if json.Unmarshal(b, &c) == nil {
				for _, at := range c.Attachments {
					pending = append(pending, at.ID)
				}
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq != initialSeq || s.failed || s.closed {
		return ErrConflict
	}
	return filepath.WalkDir(filepath.Join(s.root, "objects"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() || strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		if roots[d.Name()] {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if time.Since(info.ModTime()) < time.Hour {
			return nil
		}
		if e = os.Remove(path); e != nil {
			return e
		}
		os.Remove(path + ".json")
		return nil
	})
}

func (s *Store) Writable() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !s.failed && !s.closed }

// ObjectAvailable checks immutable object presence and size without reading a
// whole attachment. LoadObject still verifies the hash when content is opened.
func (s *Store) ObjectAvailable(id string) bool {
	if len(id) != 32 {
		return false
	}
	if _, e := hex.DecodeString(id); e != nil {
		return false
	}
	path := filepath.Join(s.root, "objects", id[:2], id)
	meta, e := os.Open(path + ".json")
	if e != nil {
		return false
	}
	defer meta.Close()
	var desc struct {
		Size int    `json:"size"`
		Hash string `json:"sha256"`
	}
	decoder := json.NewDecoder(io.LimitReader(meta, 1024))
	if decoder.Decode(&desc) != nil || decoder.Decode(new(any)) != io.EOF || desc.Size < 0 || desc.Size > 32<<20 || len(desc.Hash) != 64 {
		return false
	}
	if _, e := hex.DecodeString(desc.Hash); e != nil {
		return false
	}
	info, e := os.Stat(path)
	return e == nil && info.Mode().IsRegular() && info.Size() == int64(desc.Size)
}

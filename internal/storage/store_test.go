package storage

import (
	"bytes"
	"encoding/json"
	"github.com/a1zaza/nekoport/internal/domain"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func addMessage(t *testing.T, s *Store, id string, uid uint32) {
	t.Helper()
	e := s.Update(func(st domain.State, _ Index) ([]Op, error) {
		return []Op{Put("messages", id, domain.Message{ID: id, AccountID: "a", MailboxID: "b", UID: uid, UIDValidity: 7, Date: time.Date(2026, 1, 1, 0, 0, int(uid), 0, time.UTC)})}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestDurableRecoveryCheckpointAndIndexes(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "one", 1)
	addMessage(t, s, "two", 2)
	if e = s.Checkpoint(); e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "three", 3)
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Read(func(st domain.State, i Index) {
		if len(st.Messages) != 3 || len(i.All) != 3 || i.All[0] != "three" || i.Remote[Remote(st.Messages["two"])] != "two" || i.Unread["b"] != 3 {
			t.Fatalf("state/index mismatch: %+v", i)
		}
	})
}
func TestLockAndRelease(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if second, e := Open(dir); e == nil {
		second.Close()
		t.Fatal("second writer allowed")
	}
	s.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
}
func TestTruncatedTailPreservedAndRecovered(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "ok", 1)
	path := s.log.Name()
	s.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("{\"payload\":")
	f.Close()
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "recovery", "*.tail"))
	if len(files) != 1 {
		t.Fatal("tail was not preserved")
	}
	s.Read(func(st domain.State, _ Index) {
		if len(st.Messages) != 1 {
			t.Fatal("durable transaction lost")
		}
	})
	addMessage(t, s, "next", 2)
}
func TestCorruptionNeverSkipped(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "ok", 1)
	path := s.log.Name()
	s.Close()
	b, _ := os.ReadFile(path)
	b = bytes.Replace(b, []byte("\"ok\""), []byte("\"no\""), 1)
	os.WriteFile(path, b, 0600)
	if s, e = Open(dir); e == nil {
		s.Close()
		t.Fatal("corrupt committed record accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(b, after) {
		t.Fatal("corrupt original overwritten")
	}
}
func TestConcurrentCommitsAndRevisionConflict(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var wg sync.WaitGroup
	for n := 0; n < 30; n++ {
		wg.Go(func() {
			if e := s.Update(func(st domain.State, _ Index) ([]Op, error) {
				v := st.Settings
				v.FontSize++
				return []Op{Put("settings", "", v)}, nil
			}); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	s.Read(func(st domain.State, _ Index) {
		if st.Settings.FontSize != 45 {
			t.Fatal(st.Settings.FontSize)
		}
	})
	if e = s.Update(func(st domain.State, _ Index) ([]Op, error) { return nil, ErrConflict }); e != ErrConflict {
		t.Fatal(e)
	}
}
func TestWriteFailureFreezesStore(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.log.Close()
	update := func() error {
		return s.Update(func(st domain.State, _ Index) ([]Op, error) { return []Op{Put("settings", "", st.Settings)}, nil })
	}
	if update() == nil {
		t.Fatal("closed log committed")
	}
	if !s.failed || update() == nil {
		t.Fatal("failed writer not frozen")
	}
}
func TestCheckpointFallback(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "one", 1)
	s.Checkpoint()
	addMessage(t, s, "two", 2)
	s.Checkpoint()
	s.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "checkpoints", "committed-*"))
	var c commit
	b, _ := os.ReadFile(files[len(files)-1])
	json.Unmarshal(b, &c)
	os.WriteFile(filepath.Join(dir, "checkpoints", c.Manifest), []byte("broken"), 0600)
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Read(func(st domain.State, _ Index) {
		if len(st.Messages) != 2 {
			t.Fatal("fallback lost later journal")
		}
	})
}

func TestDuplicateSequenceRejected(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "one", 1)
	path := s.log.Name()
	s.Close()
	b, _ := os.ReadFile(path)
	os.WriteFile(path, append(b, b...), 0600)
	if s, e = Open(dir); e == nil {
		s.Close()
		t.Fatal("duplicate sequence accepted")
	}
}
func TestCollectionProtectsRecoveryAndDraftAttachments(t *testing.T) {
	s, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	attachment, e := s.Object([]byte("must survive"))
	if e != nil {
		t.Fatal(e)
	}
	orphan, e := s.Object([]byte("unreachable"))
	if e != nil {
		t.Fatal(e)
	}
	contentBytes, _ := json.Marshal(domain.Content{Text: "mail", Attachments: []domain.Attachment{{ID: attachment}}})
	content, e := s.Object(contentBytes)
	if e != nil {
		t.Fatal(e)
	}
	s.Update(func(st domain.State, _ Index) ([]Op, error) {
		return []Op{Put("messages", "m", domain.Message{ID: "m", ContentID: content})}, nil
	})
	s.Checkpoint()
	s.Update(func(st domain.State, _ Index) ([]Op, error) { return []Op{Del("messages", "m")}, nil })
	for _, id := range []string{attachment, orphan, content} {
		os.Chtimes(filepath.Join(s.root, "objects", id[:2], id), time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	}
	if e = s.Collect(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.LoadObject(attachment); e != nil {
		t.Fatal("recovery attachment collected", e)
	}
	if _, e = s.LoadObject(orphan); e == nil {
		t.Fatal("orphan not collected")
	}
	s.Checkpoint()
	s.Checkpoint()
	if e = s.Collect(); e != nil {
		t.Fatal(e)
	}
	if _, e = s.LoadObject(attachment); e == nil {
		t.Fatal("unreferenced old attachment not collected")
	}
}
func BenchmarkRecover50000(b *testing.B) {
	dir := b.TempDir()
	s, e := Open(dir)
	if e != nil {
		b.Fatal(e)
	}
	for batch := 0; batch < 100; batch++ {
		e = s.Update(func(st domain.State, _ Index) ([]Op, error) {
			ops := make([]Op, 0, 500)
			for n := 0; n < 500; n++ {
				id := ID()
				ops = append(ops, Put("messages", id, domain.Message{ID: id, AccountID: "a", MailboxID: "b", UID: uint32(batch*500 + n + 1), UIDValidity: 1, From: "测试发件人 <from@example.test>", To: "to@example.test", Subject: "中文邮件容量样本", Date: time.Unix(int64(batch*500+n), 0)}))
			}
			return ops, nil
		})
		if e != nil {
			b.Fatal(e)
		}
	}
	if e = s.Checkpoint(); e != nil {
		b.Fatal(e)
	}
	s.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, e = Open(dir)
		if e != nil {
			b.Fatal(e)
		}
		s.Close()
	}
}

func TestDamagedLatestCheckpointAndMissingTailNeverLoseCommit(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	addMessage(t, s, "one", 1)
	s.Checkpoint()
	addMessage(t, s, "two", 2)
	s.Checkpoint()
	s.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "checkpoints", "committed-*"))
	var c commit
	b, _ := os.ReadFile(files[len(files)-1])
	json.Unmarshal(b, &c)
	os.WriteFile(filepath.Join(dir, "checkpoints", c.Manifest), []byte("broken"), 0600)
	logs, _ := filepath.Glob(filepath.Join(dir, "journal", "*.jsonl"))
	for _, p := range logs {
		if filepath.Base(p) == "00000000000000000002.jsonl" {
			os.Remove(p)
		}
	}
	if s, e = Open(dir); e == nil {
		s.Close()
		t.Fatal("silently reverted an acknowledged transaction")
	}
}

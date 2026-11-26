package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/a1zaza/nekoport/internal/domain"
	mailer "github.com/a1zaza/nekoport/internal/mail"
	"github.com/a1zaza/nekoport/internal/storage"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// A message-specific limit/MIME failure can be recorded while other messages
// continue. Transport and storage failures stop the batch without advancing it.
type contentItemError struct{ error }
type downloadedContent struct {
	Content          domain.Content
	RawID, ContentID string
	CacheBytes       int64
}

func (a *App) fetchContent(c *client.Client, m domain.Message) (downloadedContent, error) {
	var out downloadedContent
	if m.Size > mailer.MaxMIME {
		return out, contentItemError{errors.New("邮件超过 30 MiB，未下载正文及附件")}
	}
	set := new(imap.SeqSet)
	set.AddNum(m.UID)
	section := &imap.BodySectionName{Peek: true}
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- c.UidFetch(set, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, ch) }()
	var raw []byte
	var readErr error
	oversize := false
	for msg := range ch {
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		if msg.Uid != m.UID {
			_, readErr = io.Copy(io.Discard, r)
			continue
		}
		raw, readErr = io.ReadAll(io.LimitReader(r, mailer.MaxMIME+1))
		if len(raw) > mailer.MaxMIME {
			oversize = true
			c.Terminate()
		}
	}
	err := <-done
	if oversize {
		return out, contentItemError{errors.New("邮件实际大小超过 30 MiB，未完成下载")}
	}
	if err != nil {
		return out, err
	}
	if readErr != nil {
		return out, readErr
	}
	if len(raw) == 0 {
		return out, contentItemError{errors.New("服务器未返回该 UID 的完整正文，等待重新对齐")}
	}
	content, e := mailer.Parse(raw, a.Store.Object)
	if e != nil {
		var path *os.PathError
		if errors.As(e, &path) {
			return out, e
		}
		return out, contentItemError{fmt.Errorf("邮件正文或附件解析失败：%w", e)}
	}
	rawID, e := a.Store.Object(raw)
	if e != nil {
		return out, e
	}
	b, e := json.Marshal(content)
	if e != nil {
		return out, e
	}
	if len(b) > 32<<20 {
		return out, contentItemError{errors.New("解析后的正文对象超过 32 MiB，未完成下载")}
	}
	contentID, e := a.Store.Object(b)
	if e != nil {
		return out, e
	}
	out = downloadedContent{Content: content, RawID: rawID, ContentID: contentID, CacheBytes: int64(len(raw) + len(b))}
	for _, at := range content.Attachments {
		out.CacheBytes += at.Size
	}
	return out, nil
}
func applyContent(m domain.Message, d downloadedContent, pinned bool) domain.Message {
	m.RawID = d.RawID
	m.ContentID = d.ContentID
	m.CacheBytes = d.CacheBytes
	m.CachedAt = time.Now().UTC()
	m.ContentPinned = m.ContentPinned || pinned
	preview := []rune(strings.Join(strings.Fields(d.Content.Text), " "))
	m.Preview = string(preview[:min(len(preview), 160)])
	m.HasAttachments = len(d.Content.Attachments) > 0
	m.Revision++
	return m
}
func (a *App) contentComplete(m domain.Message) bool {
	if m.RawID == "" || m.ContentID == "" || !a.Store.ObjectAvailable(m.RawID) {
		return false
	}
	b, e := a.Store.LoadObject(m.ContentID)
	if e != nil {
		return false
	}
	var content domain.Content
	if json.Unmarshal(b, &content) != nil {
		return false
	}
	for _, at := range content.Attachments {
		if !a.Store.ObjectAvailable(at.ID) {
			return false
		}
	}
	return true
}
func configureFullContent(t *domain.SyncTask, st domain.SyncStatus) {
	if t.Kind != "reconcile" && t.Kind != "history" || t.ContentPolicyVersion != 0 {
		return
	}
	t.ContentPolicyVersion = 1
	t.InitialFull = !st.FirstFullCompleted && st.LastFull.IsZero()
	t.DownloadContent = !t.InitialFull && (t.Trigger == "manual" || t.Trigger == "timer")
	if t.DownloadContent {
		t.Scope = "all"
		t.RecentDays = 0
	}
}

// Each content step commits object references and its UID cursor atomically.
// It shares the task's IMAP connection and cancellation deadline.
func (a *App) downloadStep(c *client.Client, x domain.Account, before domain.SyncTask) (bool, error) {
	t := before.Clone()
	t.State = "running"
	for index := range t.Folders {
		f := &t.Folders[index]
		if f.ContentDone || f.Phase != "done" {
			continue
		}
		var messages []domain.Message
		a.Store.Read(func(s domain.State, i storage.Index) {
			ids := i.UIDOrder[f.MailboxID]
			start := sort.Search(len(ids), func(n int) bool { return s.Messages[ids[n]].UID > f.ContentUID })
			for _, id := range ids[start:] {
				m := s.Messages[id]
				if m.UID > f.UpperUID {
					break
				}
				if m.UIDValidity == f.UIDValidity {
					messages = append(messages, m)
					if len(messages) == 100 {
						break
					}
				}
			}
		})
		if len(messages) == 0 {
			f.ContentDone = true
			appendSyncLog(&t, domain.SyncLog{Action: "content-done", Folder: f.Name, Message: "目录正文及附件下载阶段完成"})
			return true, a.taskCommit(before, t, nil)
		}
		// Reuse a bounded run of cached objects without opening another connection.
		var cached []domain.Message
		for _, m := range messages {
			if !a.contentComplete(m) {
				break
			}
			cached = append(cached, m)
		}
		if len(cached) > 0 {
			f.ContentUID = cached[len(cached)-1].UID
			f.Content.Reused += len(cached)
			t.Content.Reused += len(cached)
			appendSyncLog(&t, domain.SyncLog{Action: "content-reused", Folder: f.Name, Message: fmt.Sprintf("复用 %d 封已保存的正文及附件", len(cached)), FirstUID: cached[0].UID, LastUID: f.ContentUID})
			return true, a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
				var ops []storage.Op
				for _, m := range cached {
					current, ok := s.Messages[m.ID]
					if !ok {
						continue
					}
					if current.UIDValidity != m.UIDValidity || current.RawID != m.RawID || current.ContentID != m.ContentID {
						return nil, storage.ErrConflict
					}
					if !current.ContentPinned {
						current.ContentPinned = true
						current.Revision++
						ops = append(ops, storage.Put("messages", current.ID, current))
					}
				}
				return ops, nil
			})
		}
		m := messages[0]
		status, e := c.Select(f.Name, true)
		if e != nil || status.UidValidity != f.UIDValidity {
			if e == nil {
				e = errors.New("下载期间 UIDVALIDITY 变化，需要重新对齐目录")
			}
			f.ContentNeedsMetadata = true
			f.ContentDone = true
			f.Content.Failed++
			t.Content.Failed++
			appendSyncLog(&t, domain.SyncLog{Action: "content-failed", Folder: f.Name, Message: cleanError(e)})
			return true, a.taskCommit(before, t, nil)
		}
		payload, e := a.fetchContent(c, m)
		if e != nil {
			var item contentItemError
			if !errors.As(e, &item) {
				return true, e
			}
			f.ContentNeedsMetadata = true
			f.ContentUID = m.UID
			f.Content.Failed++
			t.Content.Failed++
			appendSyncLog(&t, domain.SyncLog{Action: "content-failed", Folder: f.Name, FirstUID: m.UID, LastUID: m.UID, Message: cleanError(e)})
			return true, a.taskCommit(before, t, nil)
		}
		f.ContentUID = m.UID
		f.Content.Downloaded++
		t.Content.Downloaded++
		f.Content.Bytes += payload.CacheBytes
		t.Content.Bytes += payload.CacheBytes
		appendSyncLog(&t, domain.SyncLog{Action: "content-downloaded", Folder: f.Name, FirstUID: m.UID, LastUID: m.UID, Message: "已保存完整 MIME、正文及全部 MIME 附件"})
		return true, a.taskCommit(before, t, func(s domain.State, _ storage.Index) ([]storage.Op, error) {
			current, ok := s.Messages[m.ID]
			if !ok {
				return nil, nil
			}
			if current.UIDValidity != m.UIDValidity || current.MailboxID != m.MailboxID || current.UID != m.UID {
				return nil, storage.ErrConflict
			}
			current = applyContent(current, payload, true)
			return []storage.Op{storage.Put("messages", m.ID, current)}, nil
		})
	}
	return false, nil
}

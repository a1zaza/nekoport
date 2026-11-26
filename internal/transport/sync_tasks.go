package transport

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

func publicTask(t domain.SyncTask) domain.SyncTask {
	t.RequestKey = ""
	if t.Folders == nil {
		t.Folders = []domain.SyncFolder{}
	}
	return t
}
func (s *Server) createSyncTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
		Key  string `json:"requestKey"`
	}
	if err := decode(w, r, &in); err != nil {
		failure(w, err)
		return
	}
	t, err := s.App.CreateSyncTask(r.PathValue("id"), in.Kind, "manual", in.Key)
	if err != nil {
		failure(w, err)
		return
	}
	JSON(w, 202, publicTask(t))
}
func (s *Server) syncTask(w http.ResponseWriter, r *http.Request) {
	var t domain.SyncTask
	var ok bool
	s.App.Store.Read(func(st domain.State, _ storage.Index) { t, ok = st.SyncTasks[r.PathValue("id")]; t = t.Clone() })
	if !ok {
		JSON(w, 404, map[string]string{"message": "同步任务不存在"})
		return
	}
	JSON(w, 200, publicTask(t))
}
func (s *Server) syncTasks(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	if group != "" && group != "quick" && group != "full" {
		failure(w, errors.New("同步分类无效"))
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			failure(w, errors.New("分页范围无效"))
			return
		}
		limit = n
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 1000000 {
			failure(w, errors.New("分页位置无效"))
			return
		}
		offset = n
	}
	items := []domain.SyncTask{}
	exists := false
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		_, exists = st.Accounts[r.PathValue("id")]
		for _, t := range st.SyncTasks {
			if t.AccountID == r.PathValue("id") && (group == "" || group == "quick" && t.Incremental() || group == "full" && !t.Incremental()) {
				items = append(items, publicTask(t.Clone()))
			}
		}
	})
	if !exists {
		JSON(w, 404, map[string]string{"message": "邮箱不存在"})
		return
	}
	sort.Slice(items, func(i, j int) bool {
		rank := func(t domain.SyncTask) int {
			if t.Active() {
				return 2
			}
			if t.State == "paused" {
				return 1
			}
			return 0
		}
		left, right := rank(items[i]), rank(items[j])
		if left != right {
			return left > right
		}
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	end := min(offset+limit, len(items))
	start := min(offset, len(items))
	next := 0
	if end < len(items) {
		next = end
	}
	JSON(w, 200, map[string]any{"items": items[start:end], "nextOffset": next})
}
func (s *Server) syncAction(w http.ResponseWriter, r *http.Request) {
	if err := s.App.SyncTaskAction(r.PathValue("id"), r.PathValue("action")); err != nil {
		failure(w, err)
		return
	}
	JSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) syncSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope    string `json:"scope"`
		Days     int    `json:"recentDays"`
		Enabled  bool   `json:"reconcileEnabled"`
		Interval int    `json:"intervalMinutes"`
		Revision int    `json:"expectedRevision"`
	}
	if err := decode(w, r, &in); err != nil {
		failure(w, err)
		return
	}
	if err := s.App.UpdateSyncSettings(r.PathValue("id"), in.Revision, in.Scope, in.Days, in.Enabled, in.Interval); err != nil {
		failure(w, err)
		return
	}
	JSON(w, 200, map[string]bool{"ok": true})
}

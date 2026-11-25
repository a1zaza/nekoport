package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/a1zaza/nekoport/internal/domain"
	"github.com/a1zaza/nekoport/internal/storage"
)

func TestBootstrapAdvertisesIDLEAndIgnoresLegacyQuickInterval(t *testing.T) {
	s, h := testServer(t)
	s.App.Close()
	cookie, csrf := loginTest(t, h)
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		settings := st.Settings
		settings.QuickSyncIntervalSeconds = 600
		return []storage.Op{storage.Put("settings", "", settings)}, nil
	})
	w := req(h, "PUT", "/api/v1/settings", "http://localhost:8080", map[string]any{"fontSize": 16, "autoRead": true, "cacheMiB": 1024, "quickSyncIntervalSeconds": 30}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Settings.FontSize != 16 || st.Settings.QuickSyncIntervalSeconds != 600 {
			t.Fatal("legacy interval still changes settings")
		}
	})
	w = req(h, "GET", "/api/v1/bootstrap", "", nil, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(body["settings"], &settings); err != nil {
		t.Fatal(err)
	}
	if settings["incrementalMode"] != "idle" || settings["quickSyncIntervalSeconds"] != nil || body["idleStatus"] == nil {
		t.Fatal("missing IDLE contract", w.Body.String())
	}
}

func TestSyncTaskAPIScopeActionsAndValidation(t *testing.T) {
	s, h := testServer(t)
	s.App.Close()
	cookie, csrf := loginTest(t, h)
	s.App.Store.Update(func(st domain.State, _ storage.Index) ([]storage.Op, error) {
		x := st.Accounts["test-account"]
		x.SyncDays = 30
		x.Revision = 1
		return []storage.Op{storage.Put("accounts", x.ID, x)}, nil
	})
	payload := map[string]any{"kind": "history", "requestKey": "same-manual-request"}
	w := req(h, "POST", "/api/v1/accounts/test-account/sync-tasks", "http://localhost:8080", payload, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "same-manual-request") {
		t.Fatal("private key leaked")
	}
	var task domain.SyncTask
	json.Unmarshal(w.Body.Bytes(), &task)
	if task.ID == "" || task.Folders == nil {
		t.Fatal("invalid task DTO")
	}
	second := req(h, "POST", "/api/v1/accounts/test-account/sync-tasks", "http://localhost:8080", payload, cookie, csrf)
	var same domain.SyncTask
	json.Unmarshal(second.Body.Bytes(), &same)
	if same.ID != task.ID {
		t.Fatal("duplicate created")
	}
	if w = req(h, "POST", "/api/v1/sync-tasks/"+task.ID+"/cancel", "http://localhost:8080", nil, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = req(h, "POST", "/api/v1/sync-tasks/"+task.ID+"/resume", "http://localhost:8080", nil, cookie, csrf); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = req(h, "GET", "/api/v1/accounts/test-account/sync-tasks?limit=0", "", nil, cookie, "")
	if w.Code != 400 {
		t.Fatal("invalid limit accepted")
	}
	w = req(h, "GET", "/api/v1/accounts/test-account/sync-tasks?limit=1", "", nil, cookie, "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.App.Store.Read(func(st domain.State, _ storage.Index) {
		if st.Accounts["test-account"].Scope() != "all" {
			t.Fatal("history scope not atomic")
		}
	})
	w = req(h, "PUT", "/api/v1/accounts/test-account/sync-settings", "http://localhost:8080", map[string]any{"scope": "recent", "recentDays": 20, "reconcileEnabled": false, "intervalMinutes": 15, "expectedRevision": 1}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("stale config revision accepted", w.Code)
	}
}

func TestSyncLogGroupsAreIsolatedAndDetailsAreAvailable(t *testing.T) {
	s, h := testServer(t)
	s.App.Close()
	cookie, _ := loginTest(t, h)
	err := s.App.Store.Update(func(_ domain.State, _ storage.Index) ([]storage.Op, error) {
		var ops []storage.Op
		for _, entry := range []struct{ id, account, kind string }{{"quick", "test-account", "quick"}, {"full", "test-account", "reconcile"}, {"history", "test-account", "history"}, {"private", "other-account", "quick"}} {
			task := domain.SyncTask{ID: entry.id, AccountID: entry.account, Kind: entry.kind, State: "succeeded", LogVersion: 1, Stats: domain.SyncCounters{Compared: 8, Added: 3, Deleted: 2}, Logs: []domain.SyncLog{{Action: "delete-confirm", Message: "二次查询确认删除", Stats: domain.SyncCounters{Deleted: 2}}}}
			ops = append(ops, storage.Put("syncTasks", task.ID, task))
		}
		return ops, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"quick", "full"} {
		w := req(h, "GET", "/api/v1/accounts/test-account/sync-tasks?group="+group, "", nil, cookie, "")
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var result struct {
			Items []domain.SyncTask `json:"items"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		want := 1
		if group == "full" {
			want = 2
		}
		if len(result.Items) != want {
			t.Fatal(group, len(result.Items))
		}
		for _, task := range result.Items {
			if task.ID == "private" || group == "quick" && task.Kind != "quick" || group == "full" && task.Kind == "quick" {
				t.Fatal("wrong category", task)
			}
			if task.Stats.Deleted != 2 || len(task.Logs) != 1 {
				t.Fatal("detail missing")
			}
		}
	}
	w := req(h, "GET", "/api/v1/accounts/test-account/sync-tasks?group=invalid", "", nil, cookie, "")
	if w.Code != 400 {
		t.Fatal("invalid group accepted")
	}
}

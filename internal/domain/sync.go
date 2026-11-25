package domain

import "time"

type SyncFolder struct {
	SnapshotAt  time.Time    `json:"snapshotAt,omitempty"`
	LocalBefore int          `json:"localBefore"`
	RemoteTotal uint32       `json:"remoteTotal"`
	Stats       SyncCounters `json:"stats"`
	MailboxID   string       `json:"mailboxId"`
	Name        string       `json:"name"`
	UIDValidity uint32       `json:"uidValidity"`
	UpperUID    uint32       `json:"upperUid"`
	NextUID     uint64       `json:"nextUid"`
	Phase       string       `json:"phase"`
	Processed   int          `json:"processed"`
	Error       string       `json:"error,omitempty"`
}

// Counters describe committed comparisons, including repeated checks on retry.
type SyncCounters struct {
	Skipped   int `json:"skipped"`
	Compared  int `json:"compared"`
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Deleted   int `json:"deleted"`
}

type SyncLog struct {
	At       time.Time    `json:"at"`
	Action   string       `json:"action"`
	Folder   string       `json:"folder,omitempty"`
	Message  string       `json:"message"`
	FirstUID uint32       `json:"firstUid,omitempty"`
	LastUID  uint32       `json:"lastUid,omitempty"`
	Stats    SyncCounters `json:"stats"`
}

type SyncTask struct {
	LogVersion      int          `json:"logVersion,omitempty"`
	Scope           string       `json:"scope,omitempty"`
	RecentDays      int          `json:"recentDays,omitempty"`
	StartedAt       time.Time    `json:"startedAt,omitempty"`
	Stats           SyncCounters `json:"stats"`
	Logs            []SyncLog    `json:"logs,omitempty"`
	LogsDropped     int          `json:"logsDropped,omitempty"`
	Generation      string       `json:"generation"`
	ID              string       `json:"id"`
	AccountID       string       `json:"accountId"`
	Kind            string       `json:"kind"`
	Trigger         string       `json:"trigger"`
	RequestKey      string       `json:"requestKey,omitempty"`
	State           string       `json:"state"`
	AccountRevision int          `json:"accountRevision"`
	Folders         []SyncFolder `json:"folders"`
	Discovered      bool         `json:"discovered"`
	CreatedAt       time.Time    `json:"createdAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	FinishedAt      time.Time    `json:"finishedAt,omitempty"`
	Error           string       `json:"error,omitempty"`
}
type SyncStatus struct {
	AccountID        string    `json:"accountId"`
	LastQuick        time.Time `json:"lastQuickSuccessAt"`
	LastFull         time.Time `json:"lastFullSuccessAt"`
	NextQuick        time.Time `json:"nextQuickAt"`
	NextFull         time.Time `json:"nextDueAt"`
	RetryAt          time.Time `json:"retryAt"`
	Failures         int       `json:"failures"`
	ErrorKind        string    `json:"errorKind,omitempty"`
	Error            string    `json:"error,omitempty"`
	PendingReconcile bool      `json:"pendingReconcile"`
}

func (t SyncTask) Active() bool { return t.State == "queued" || t.State == "running" }
func (t SyncTask) Clone() SyncTask {
	t.Folders = append([]SyncFolder(nil), t.Folders...)
	t.Logs = append([]SyncLog(nil), t.Logs...)
	return t
}
func (a Account) Scope() string {
	if a.SyncScope == "all" {
		return "all"
	}
	return "recent"
}
func (a Account) ReconcileMinutes() int {
	if a.ReconcileIntervalMinutes < 1 {
		return 15
	}
	return a.ReconcileIntervalMinutes
}
func (a Account) ReconcileOn() bool { return a.ReconcileEnabled == nil || *a.ReconcileEnabled }
func (s Settings) QuickSeconds() int {
	if s.QuickSyncIntervalSeconds < 30 {
		return 60
	}
	return s.QuickSyncIntervalSeconds
}

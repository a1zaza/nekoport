package domain

import "time"

type Endpoint struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
}
type Account struct {
	IMAPMaxConnections       int      `json:"imapMaxConnections,omitempty"`
	SyncScope                string   `json:"syncScope,omitempty"`
	ReconcileEnabled         *bool    `json:"reconcileEnabled,omitempty"`
	ReconcileIntervalMinutes int      `json:"reconcileIntervalMinutes,omitempty"`
	ID                       string   `json:"id"`
	Name                     string   `json:"name"`
	Address                  string   `json:"address"`
	IMAP                     Endpoint `json:"imap"`
	SMTP                     Endpoint `json:"smtp"`
	CredentialRef            string   `json:"credentialRef"`
	Paused                   bool     `json:"paused"`
	SyncDays                 int      `json:"syncDays"`
	AutoArchive              bool     `json:"autoArchive"`
	LastSync                 string   `json:"lastSync,omitempty"`
	Error                    string   `json:"error,omitempty"`
	Revision                 int      `json:"revision"`
}
type Mailbox struct {
	BaselineVersion  int       `json:"baselineVersion,omitempty"`
	Initialized      bool      `json:"initialized,omitempty"`
	NoSelect         bool      `json:"noSelect,omitempty"`
	LastObservedUID  uint32    `json:"lastObservedUid,omitempty"`
	LastReconciledAt time.Time `json:"lastReconciledAt,omitempty"`
	ID               string    `json:"id"`
	AccountID        string    `json:"accountId"`
	Name             string    `json:"name"`
	Role             string    `json:"role"`
	UIDValidity      uint32    `json:"uidValidity"`
}
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	CID  string `json:"cid,omitempty"`
}
type Message struct {
	ContentPinned      bool      `json:"contentPinned,omitempty"`
	FromName           string    `json:"fromName,omitempty"`
	FromDisplay        string    `json:"fromDisplay,omitempty"`
	ToDisplay          string    `json:"toDisplay,omitempty"`
	CCDisplay          string    `json:"ccDisplay,omitempty"`
	LastSeenGeneration string    `json:"lastSeenGeneration,omitempty"`
	ReplyTo            string    `json:"replyTo,omitempty"`
	ID                 string    `json:"id"`
	AccountID          string    `json:"accountId"`
	MailboxID          string    `json:"mailboxId"`
	UID                uint32    `json:"uid"`
	UIDValidity        uint32    `json:"uidValidity"`
	MessageID          string    `json:"messageId"`
	From               string    `json:"from"`
	To                 string    `json:"to"`
	CC                 string    `json:"cc"`
	Subject            string    `json:"subject"`
	Date               time.Time `json:"date"`
	Preview            string    `json:"preview"`
	Seen               bool      `json:"seen"`
	Starred            bool      `json:"starred"`
	HasAttachments     bool      `json:"hasAttachments"`
	Size               uint32    `json:"size"`
	RawID              string    `json:"rawId,omitempty"`
	ContentID          string    `json:"contentId,omitempty"`
	Revision           int       `json:"revision"`
	CacheBytes         int64     `json:"cacheBytes,omitempty"`
	CachedAt           time.Time `json:"cachedAt,omitempty"`
}
type Content struct {
	HTMLVersion int          `json:"htmlVersion,omitempty"`
	Text        string       `json:"text"`
	HTML        string       `json:"html"`
	Attachments []Attachment `json:"attachments"`
}
type Draft struct {
	ID          string       `json:"id"`
	AccountID   string       `json:"accountId"`
	To          string       `json:"to"`
	CC          string       `json:"cc"`
	BCC         string       `json:"bcc"`
	Subject     string       `json:"subject"`
	BodyID      string       `json:"bodyId"`
	Attachments []Attachment `json:"attachments"`
	InReplyTo   string       `json:"inReplyTo,omitempty"`
	Revision    int          `json:"revision"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}
type SendJob struct {
	FromDisplay string    `json:"fromDisplay,omitempty"`
	ToDisplay   string    `json:"toDisplay,omitempty"`
	Subject     string    `json:"subject"`
	To          string    `json:"to"`
	ID          string    `json:"id"`
	DraftID     string    `json:"draftId"`
	AccountID   string    `json:"accountId"`
	Key         string    `json:"key"`
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	RawID       string    `json:"rawId"`
	Recipients  []string  `json:"recipients"`
	Archive     string    `json:"archive"`
	CreatedAt   time.Time `json:"createdAt"`
}
type Operation struct {
	Optimistic        bool      `json:"optimistic,omitempty"`
	PreviousValue     bool      `json:"previousValue,omitempty"`
	TargetUIDFloor    uint32    `json:"targetUidFloor,omitempty"`
	TargetUIDValidity uint32    `json:"targetUidValidity,omitempty"`
	ID                string    `json:"id"`
	AccountID         string    `json:"accountId"`
	MessageID         string    `json:"messageId"`
	Kind              string    `json:"kind"`
	Value             bool      `json:"value"`
	Target            string    `json:"target,omitempty"`
	State             string    `json:"state"`
	Error             string    `json:"error,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}
type Settings struct {
	AuthRevision int `json:"authRevision"`
	// Legacy checkpoint field; ignored by the IDLE scheduler.
	QuickSyncIntervalSeconds int    `json:"quickSyncIntervalSeconds"`
	Username                 string `json:"username"`
	PasswordHash             string `json:"passwordHash"`
	FontSize                 int    `json:"fontSize"`
	AutoRead                 bool   `json:"autoRead"`
	CacheMiB                 int    `json:"cacheMiB"`
}
type State struct {
	SyncTasks  map[string]SyncTask   `json:"syncTasks"`
	SyncStatus map[string]SyncStatus `json:"syncStatus"`
	Settings   Settings              `json:"settings"`
	Accounts   map[string]Account    `json:"accounts"`
	Mailboxes  map[string]Mailbox    `json:"mailboxes"`
	Messages   map[string]Message    `json:"messages"`
	Drafts     map[string]Draft      `json:"drafts"`
	Jobs       map[string]SendJob    `json:"jobs"`
	Operations map[string]Operation  `json:"operations"`
}

func Empty() State {
	return State{SyncTasks: map[string]SyncTask{}, SyncStatus: map[string]SyncStatus{}, Settings: Settings{FontSize: 15, AutoRead: true, CacheMiB: 1024}, Accounts: map[string]Account{}, Mailboxes: map[string]Mailbox{}, Messages: map[string]Message{}, Drafts: map[string]Draft{}, Jobs: map[string]SendJob{}, Operations: map[string]Operation{}}
}

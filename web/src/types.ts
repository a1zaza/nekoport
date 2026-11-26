export type Endpoint = {
  host: string;
  port: number;
  tls: "tls" | "starttls";
  username: string;
};
export type Account = {
  imapMaxConnections?: number;
  syncScope?: "recent" | "all";
  reconcileEnabled?: boolean;
  reconcileIntervalMinutes?: number;
  id: string;
  name: string;
  address: string;
  imap: Endpoint;
  smtp: Endpoint;
  credentialRef: string;
  paused: boolean;
  syncDays: number;
  autoArchive: boolean;
  lastSync?: string;
  error?: string;
  revision: number;
};
export type Mailbox = {
  id: string;
  accountId: string;
  name: string;
  role: string;
  unread: number;
};
export type Attachment = {
  id: string;
  name: string;
  type: string;
  size: number;
  cid?: string;
};
export type Message = {
  fromName?: string;
  fromDisplay?: string;
  toDisplay?: string;
  ccDisplay?: string;
  replyTo?: string;
  id: string;
  accountId: string;
  mailboxId: string;
  uid: number;
  uidValidity: number;
  messageId: string;
  from: string;
  to: string;
  cc: string;
  subject: string;
  date: string;
  preview: string;
  seen: boolean;
  starred: boolean;
  hasAttachments: boolean;
  revision: number;
};
export type Content = { text: string; html: string; attachments: Attachment[] };
export type Draft = {
  id: string;
  accountId: string;
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  bodyId: string;
  attachments: Attachment[];
  inReplyTo?: string;
  revision: number;
  updatedAt: string;
};
export type Job = {
  fromDisplay?: string;
  toDisplay?: string;
  subject?: string;
  to?: string;
  id: string;
  draftId: string;
  accountId: string;
  state: string;
  archive: string;
  error?: string;
  createdAt: string;
};
export type Operation = {
  id: string;
  accountId: string;
  messageId: string;
  kind: string;
  state: string;
  error?: string;
};
export type Settings = {
  incrementalMode?: "idle";
  fontSize: number;
  autoRead: boolean;
  cacheMiB: number;
};
export type Bootstrap = {
  authAccount?: { username: string; revision: number };
  syncStatus?: Record<string, SyncStatus>;
  syncTasks?: SyncTask[];
  storage?: { writable: boolean };
  accounts: Account[];
  mailboxes: Mailbox[];
  drafts: Draft[];
  jobs: Job[];
  operations: Operation[];
  settings: Settings;
  idleStatus?: Record<string, IdleStatus>;
  running: Record<string, boolean>;
  total: number;
};

export type IdleStatus = {
  state: string;
  connected: number;
  total: number;
  limitedFolders?: string[];
  error?: string;
  retryAt?: string;
  lastEventAt?: string;
};

export type SyncStatus = {
  lastQuickSuccessAt?: string;
  lastFullSuccessAt?: string;
  nextQuickAt?: string;
  nextDueAt?: string;
  retryAt?: string;
  errorKind?: string;
  error?: string;
};
export type SyncCounters = {
  skipped?: number;
  compared: number;
  added: number;
  updated: number;
  unchanged: number;
  deleted: number;
};
export type SyncLog = {
  at: string;
  action: string;
  folder?: string;
  message: string;
  firstUid?: number;
  lastUid?: number;
  stats: SyncCounters;
};
export type SyncTask = {
  retryAt?: string;
  scopeType?: string;
  reasons?: string[];
  logVersion?: number;
  scope?: string;
  recentDays?: number;
  startedAt?: string;
  createdAt?: string;
  updatedAt?: string;
  finishedAt?: string;
  trigger?: string;
  stats?: SyncCounters;
  logs?: SyncLog[];
  logsDropped?: number;
  id: string;
  accountId: string;
  kind: string;
  state: string;
  error?: string;
  folders: {
    mailboxId: string;
    name: string;
    phase: string;
    processed: number;
    snapshotAt?: string;
    localBefore?: number;
    remoteTotal?: number;
    upperUid?: number;
    nextUid?: number;
    stats?: SyncCounters;
    error?: string;
  }[];
};

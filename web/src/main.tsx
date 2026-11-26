import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { createRoot } from "react-dom/client";
import {
  Mail,
  LayoutDashboard,
  Inbox,
  Star,
  FileText,
  Send,
  Settings2,
  Plus,
  Search,
  RefreshCw,
  ChevronDown,
  ChevronRight,
  ChevronLeft,
  Paperclip,
  Trash2,
  Archive,
  Folder,
  Menu,
  X,
  Reply,
  ReplyAll,
  Forward,
  MoreHorizontal,
  LogOut,
  AlertCircle,
  Check,
  CheckCheck,
  ArrowDownToLine,
  MoveRight,
  Eye,
  ShieldCheck,
  Server,
  Clock,
  SlidersHorizontal,
} from "lucide-react";
import { api, download, setCSRF, size, time, isAbortError } from "./api";
import { Modal, Select, Empty, ErrorBox, Field } from "./components";
import { AccountEditor } from "./AccountEditor";
import { Compose, type ComposeValue } from "./Compose";
import { mailDocument } from "./html";
import { MailFrame } from "./MailFrame";
import { Overview } from "./Overview";
import { AccountSecurity } from "./AccountSecurity";
import { SyncPanel } from "./SyncPanel";
import { useBackgroundRefresh } from "./useBackgroundRefresh";
import type {
  Account,
  Bootstrap,
  Content,
  Draft,
  Message,
  Settings,
} from "./types";
import "./style.css";
const labels: Record<string, string> = {
  inbox: "收件箱",
  sent: "已发送",
  trash: "已删除",
  junk: "垃圾邮件",
  drafts: "服务器草稿",
  archive: "归档",
};
const mailboxOrder: Record<string, number> = {
  inbox: 0,
  drafts: 1,
  sent: 2,
  archive: 3,
  junk: 4,
  trash: 5,
};
const jobNames: Record<string, string> = {
  queued: "等待发送",
  sending: "连接服务器",
  submitting: "提交邮件中",
  sent: "已发送",
  failed: "发送失败",
  unknown: "结果待确认",
  cancelled: "已取消",
};
function newDraft(accountId: string): Draft {
  return {
    id: "",
    accountId,
    to: "",
    cc: "",
    bcc: "",
    subject: "",
    bodyId: "",
    attachments: [],
    revision: 0,
    updatedAt: "",
  };
}
function App() {
  const [auth, setAuth] = useState<boolean | null>(null),
    [data, setData] = useState<Bootstrap | null>(null),
    [view, setView] = useState("overview"),
    [mailbox, setMailbox] = useState(""),
    [accountScope, setAccountScope] = useState(""),
    [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [bodySearch, setBodySearch] = useState(false),
    [unread, setUnread] = useState(false),
    [attachments, setAttachments] = useState(false),
    [filters, setFilters] = useState(false),
    [after, setAfter] = useState(""),
    [before, setBefore] = useState(""),
    [items, setItems] = useState<Message[]>([]),
    [cursor, setCursor] = useState(""),
    [partial, setPartial] = useState(false),
    [selected, setSelected] = useState(""),
    [detail, setDetail] = useState<{
      message: Message;
      content: Content;
    } | null>(null),
    [detailLoading, setDetailLoading] = useState(false),
    [listLoading, setListLoading] = useState(false),
    [error, setError] = useState(""),
    [detailError, setDetailError] = useState(""),
    [archiveBusy, setArchiveBusy] = useState(false),
    [listError, setListError] = useState(""),
    [tick, setTick] = useState(0),
    [editor, setEditor] = useState<Account | null | undefined>(undefined),
    [compose, setCompose] = useState<ComposeValue | null>(null),
    [navOpen, setNavOpen] = useState(false),
    [expandedAccount, setExpandedAccount] = useState(""),
    [checked, setChecked] = useState<Set<string>>(new Set()),
    [plain, setPlain] = useState(false),
    [remote, setRemote] = useState(false),
    [confirm, setConfirm] = useState<{
      title: string;
      text: string;
      action: () => Promise<void>;
    } | null>(null),
    [move, setMove] = useState(false),
    [moveTarget, setMoveTarget] = useState(""),
    [notice, setNotice] = useState(""),
    [confirmBusy, setConfirmBusy] = useState(false),
    [listWidth, setListWidth] = useState(
      () => Number(localStorage.getItem("nekoport-list-width")) || 360,
    );
  const [cidImages, setCIDImages] = useState<Record<string, string>>({});
  const loadingRef = useRef(false);
  const detailID = useRef("");
  const operationEpoch = useRef(0);
  const accounts = data?.accounts || [];
  const mailView = !["overview", "drafts", "outbox", "settings"].includes(view);
  const settings = data?.settings;
  const {
    refresh,
    error: refreshError,
    clearError: clearRefreshError,
    stop: stopRefresh,
  } = useBackgroundRefresh(
    (signal) => api<Bootstrap>("/bootstrap", "GET", undefined, signal),
    (value) => {
      if (
        !value ||
        ![value.accounts, value.mailboxes, value.drafts, value.jobs].every(
          Array.isArray,
        )
      )
        throw new Error("页面数据响应无效");
      value.accounts.sort((a, b) => a.name.localeCompare(b.name));
      value.mailboxes.sort(
        (a, b) =>
          (mailboxOrder[a.role] ?? 6) - (mailboxOrder[b.role] ?? 6) ||
          a.name.localeCompare(b.name, "zh-CN", { numeric: true }) ||
          a.id.localeCompare(b.id),
      );
      value.drafts.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
      value.jobs.sort((a, b) => b.createdAt.localeCompare(a.createdAt));
      setData(value);
      setExpandedAccount((current) =>
        value.accounts.some((account) => account.id === current) ? current : "",
      );
      setTick((t) => t + 1);
    },
  );
  useEffect(() => {
    api<{ authenticated: boolean; csrf: string }>("/session")
      .then((s) => {
        setCSRF(s.csrf);
        setAuth(s.authenticated);
      })
      .catch((e) => {
        setAuth(false);
        setError(e.message);
      });
    const expired = () => {
      setAuth(false);
      setData(null);
    };
    window.addEventListener("session-expired", expired);
    return () => window.removeEventListener("session-expired", expired);
  }, []);
  useEffect(() => {
    if (!auth) return;
    void refresh();
    const es = new EventSource("/api/v1/events");
    es.addEventListener("refresh", () => void refresh());
    es.onerror = () => setNotice("实时连接正在重连，后台任务仍会保存结果");
    es.onopen = () => setNotice("");
    const online = () => void refresh();
    window.addEventListener("online", online);
    return () => {
      es.close();
      window.removeEventListener("online", online);
      stopRefresh();
      clearRefreshError();
    };
  }, [auth, refresh, stopRefresh, clearRefreshError]);
  useEffect(() => {
    const t = setTimeout(() => setQuery(search.trim()), 300);
    return () => clearTimeout(t);
  }, [search]);
  const params = useMemo(() => {
    const p = new URLSearchParams();
    if (mailbox) p.set("mailbox", mailbox);
    else if (["inbox", "starred"].includes(view)) p.set("view", view);
    if (accountScope) p.set("account", accountScope);
    if (query) p.set("q", query);
    if (bodySearch) p.set("body", "true");
    if (unread) p.set("unread", "true");
    if (attachments) p.set("attachments", "true");
    if (after) p.set("after", after);
    if (before) p.set("before", before);
    return p.toString();
  }, [
    view,
    mailbox,
    accountScope,
    query,
    bodySearch,
    unread,
    attachments,
    after,
    before,
  ]);
  useEffect(() => {
    if (!auth || !mailView) return;
    const controller = new AbortController();
    const epoch = operationEpoch.current;
    setListLoading(true);
    api<{ items: Message[]; nextCursor: string; partial: boolean }>(
      `/messages?${params}`,
      "GET",
      undefined,
      controller.signal,
    )
      .then((v) => {
        if (controller.signal.aborted || epoch !== operationEpoch.current)
          return;
        if (!v || !Array.isArray(v.items)) throw new Error("邮件列表响应无效");
        setItems(v.items);
        // Selection belongs to the visible list, not to removed/filtered rows.
        const visible = new Set(v.items.map((message) => message.id));
        setChecked((current) => {
          const next = new Set([...current].filter((id) => visible.has(id)));
          return next.size === current.size ? current : next;
        });
        setListError("");
        setCursor(v.nextCursor);
        setPartial(v.partial);
      })
      .catch((e) => {
        if (
          !controller.signal.aborted &&
          epoch === operationEpoch.current &&
          !isAbortError(e)
        )
          setListError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setListLoading(false);
      });
    return () => controller.abort();
  }, [auth, mailView, params, tick]);
  useEffect(() => {
    setChecked(new Set());
    setSelected("");
    setDetail(null);
  }, [params]);
  useEffect(() => {
    if (!selected) return;
    detailID.current = selected;
    setDetailLoading(true);
    setDetailError("");
    setDetail(null);
    setPlain(false);
    setRemote(false);
    const controller = new AbortController();
    api<{ message: Message; content: Content }>(
      `/messages/${selected}`,
      "GET",
      undefined,
      controller.signal,
    )
      .then((v) => {
        if (controller.signal.aborted || detailID.current !== selected) return;
        setDetail(v);
        if (settings?.autoRead && !v.message.seen) {
          void api("/message-operations", "POST", {
            ids: [selected],
            kind: "seen",
            value: true,
            target: "",
          })
            .then(() => {
              applyLocalOperation([selected], "seen", true);
              void refresh();
            })
            .catch((e) => setError(e.message));
        }
      })
      .catch((e) => {
        if (e.name !== "AbortError") setDetailError(e.message);
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [selected]);
  async function act(fn: () => Promise<unknown>) {
    setError("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }
  async function more() {
    if (!cursor || loadingRef.current) return;
    loadingRef.current = true;
    const epoch = operationEpoch.current;
    setListLoading(true);
    try {
      const v = await api<{
        items: Message[];
        nextCursor: string;
        partial: boolean;
      }>(`/messages?${params}&cursor=${encodeURIComponent(cursor)}`);
      if (epoch !== operationEpoch.current) return;
      if (!v || !Array.isArray(v.items)) throw new Error("邮件列表响应无效");
      setItems((old) => [
        ...old,
        ...v.items.filter((m) => !old.some((x) => x.id === m.id)),
      ]);
      setListError("");
      setCursor(v.nextCursor);
      setPartial(v.partial);
    } catch (e) {
      if (epoch === operationEpoch.current && !isAbortError(e))
        setListError((e as Error).message);
    } finally {
      loadingRef.current = false;
      setListLoading(false);
    }
  }
  function navigate(v: string, box = "", account = "") {
    setView(v);
    setListError("");
    setMailbox(box);
    setAccountScope(account);
    setSelected("");
    setDetail(null);
    setNavOpen(false);
  }
  function startCompose() {
    if (!accounts.length) {
      setEditor(null);
      return;
    }
    setCompose({ draft: newDraft(accounts[0].id), body: "" });
  }
  async function openDraft(id: string) {
    await act(async () => {
      const d = await api<ComposeValue>(`/drafts/${id}`);
      setCompose(d);
    });
  }
  function reply(kind: "reply" | "all" | "forward") {
    if (!detail) return;
    const m = detail.message;
    if (kind === "forward") {
      void act(async () => {
        const forwarded = await api<ComposeValue>(
          `/messages/${m.id}/forward`,
          "POST",
        );
        setCompose(forwarded);
      });
      return;
    }
    let d = newDraft(m.accountId);
    const subject = m.subject || "";
    d.subject = /^re:/i.test(subject) ? subject : `Re: ${subject}`;
    const own = accounts.map((a) => a.address.toLowerCase());
    const rawFrom = m.replyTo || m.from;
    d.to = rawFrom;
    if (kind === "all") {
      const cc = [m.to, m.cc]
        .filter(Boolean)
        .join(", ")
        .split(/,(?=(?:[^\"]*\"[^\"]*\")*[^\"]*$)/)
        .filter(
          (v) =>
            !own.includes(
              (v.match(/<([^<>]+)>/)?.[1] || v.trim()).toLowerCase(),
            ),
        );
      d.cc = cc.join(", ");
    }
    d.inReplyTo = m.messageId;
    const quote = detail.content.text
      .split("\n")
      .map((l) => `> ${l}`)
      .join("\n");
    setCompose({
      draft: d,
      body: `\n\n${time(m.date, true)}，${m.fromDisplay || m.from} 写道：\n${quote}`,
    });
  }
  // Apply only after the intent is durably queued, without waiting for refresh/IMAP.
  function applyLocalOperation(ids: string[], kind: string, value = false) {
    operationEpoch.current++;
    const targets = new Set(ids);
    const moving = kind === "move" || kind === "trash";
    const known = new Map(items.map((m) => [m.id, m]));
    if (detail?.message && !known.has(detail.message.id))
      known.set(detail.message.id, detail.message);
    const patch = (m: Message) =>
      !targets.has(m.id)
        ? m
        : {
            ...m,
            ...(kind === "seen" ? { seen: value } : {}),
            ...(kind === "starred" ? { starred: value } : {}),
          };
    setItems((current) =>
      current
        .map(patch)
        .filter(
          (m) =>
            !(moving && targets.has(m.id)) &&
            !(unread && m.seen) &&
            !(view === "starred" && !m.starred),
        ),
    );
    setDetail((current) =>
      current ? { ...current, message: patch(current.message) } : current,
    );
    setData((current) => {
      if (!current) return current;
      const changes = new Map<string, number>();
      let removed = 0;
      for (const id of targets) {
        const m = known.get(id);
        if (!m) continue;
        if (moving) removed++;
        const delta = moving
          ? m.seen
            ? 0
            : -1
          : kind === "seen"
            ? Number(!value) - Number(!m.seen)
            : 0;
        changes.set(m.mailboxId, (changes.get(m.mailboxId) || 0) + delta);
      }
      return {
        ...current,
        total: Math.max(0, current.total - removed),
        mailboxes: current.mailboxes.map((b) => ({
          ...b,
          unread: Math.max(0, b.unread + (changes.get(b.id) || 0)),
        })),
      };
    });
  }
  function completeOperation(ids: string[], closeReader = false) {
    const completed = new Set(ids);
    // A request may finish after the user has selected other messages.
    setChecked((current) => {
      const next = new Set([...current].filter((id) => !completed.has(id)));
      return next.size === current.size ? current : next;
    });
    if (closeReader)
      setSelected((current) => (completed.has(current) ? "" : current));
  }
  async function operation(
    kind: string,
    value = false,
    target = "",
    ids?: string[],
  ) {
    const targets = ids ? [...ids] : [...checked];
    if (!targets.length && selected) targets.push(selected);
    if (!targets.length) return;
    return act(async () => {
      await api("/message-operations", "POST", {
        ids: targets,
        kind,
        value,
        target,
      });
      applyLocalOperation(targets, kind, value);
      completeOperation(targets, kind === "move" || kind === "trash");
    });
  }
  function deleteMail() {
    const ids = checked.size ? [...checked] : [selected];
    if (!ids[0]) return;
    setConfirm({
      title: "移到已删除",
      text: `将 ${ids.length} 封邮件移到所属邮箱的已删除文件夹？`,
      action: async () => {
        await api("/message-operations", "POST", {
          ids,
          kind: "trash",
          value: false,
          target: "",
        });
        applyLocalOperation(ids, "trash");
        completeOperation(ids, true);
        void refresh();
      },
    });
  }
  const currentBox = data?.mailboxes.find((b) => b.id === mailbox);
  const title =
    view === "settings"
      ? "设置"
      : view === "drafts"
        ? "草稿"
        : view === "outbox"
          ? "发件箱"
          : currentBox
            ? labels[currentBox.role] || currentBox.name
            : view === "starred"
              ? "星标邮件"
              : "所有收件箱";
  const selectedMessage =
    items.find((m) => m.id === selected) || detail?.message;
  const archiveBox = data?.mailboxes.find(
    (b) => b.accountId === selectedMessage?.accountId && b.role === "archive",
  );
  const alreadyArchived =
    data?.mailboxes.some(
      (b) => b.id === selectedMessage?.mailboxId && b.role === "archive",
    ) || false;
  async function archiveMail() {
    if (archiveBusy || !selectedMessage || !archiveBox || alreadyArchived)
      return;
    const id = selectedMessage.id,
      target = archiveBox.id;
    setArchiveBusy(true);
    try {
      await act(async () => {
        await api("/message-operations", "POST", {
          ids: [id],
          kind: "move",
          value: false,
          target,
        });
        applyLocalOperation([id], "move");
        completeOperation([id], true);
      });
    } finally {
      setArchiveBusy(false);
    }
  }
  const unreadTotal =
    data?.mailboxes
      .filter((b) => b.role === "inbox")
      .reduce((a, b) => a + b.unread, 0) || 0;
  const working = Object.values(data?.running || {}).some(Boolean);
  useEffect(() => {
    const controller = new AbortController();
    const urls: string[] = [];
    setCIDImages({});
    if (detail) {
      void (async () => {
        const images: Record<string, string> = {};
        let total = 0;
        for (const at of detail.content.attachments) {
          if (
            !at.cid ||
            !/^image\/(png|jpeg|gif|webp)$/.test(at.type) ||
            at.size > 5 * 1024 * 1024 ||
            total + at.size > 10 * 1024 * 1024
          )
            continue;
          total += at.size;
          try {
            const response = await fetch(
              download(`/messages/${detail.message.id}/attachments/${at.id}`),
              { signal: controller.signal, credentials: "same-origin" },
            );
            if (!response.ok) continue;
            const b = await response.arrayBuffer();
            if (controller.signal.aborted) break;
            const url = URL.createObjectURL(new Blob([b], { type: at.type }));
            urls.push(url);
            images[at.cid] = url;
          } catch {}
        }
        if (!controller.signal.aborted) setCIDImages(images);
      })();
    }
    return () => {
      controller.abort();
      urls.forEach((url) => URL.revokeObjectURL(url));
    };
  }, [detail]);
  const frameHTML = useMemo(
    () =>
      detail
        ? mailDocument(
            detail.content.html,
            remote,
            settings?.fontSize || 15,
            cidImages,
          )
        : "",
    [detail, remote, settings?.fontSize, cidImages],
  );
  if (auth === null)
    return (
      <div className="app-loading">
        <Mail size={32} />
        <p>正在连接 nekoport…</p>
      </div>
    );
  if (!auth)
    return (
      <Login
        error={error}
        notice={notice}
        onLogin={() => {
          setError("");
          navigate("overview");
          setAuth(true);
        }}
      />
    );
  return (
    <div
      className="app-shell"
      style={{ "--list-width": `${listWidth}px` } as React.CSSProperties}
    >
      <header className="topbar">
        <button
          className="icon-button mobile-nav"
          aria-label="展开邮箱导航"
          onClick={() => setNavOpen(true)}
        >
          <Menu size={20} />
        </button>
        <a
          className="brand"
          href="/"
          onClick={(e) => {
            e.preventDefault();
            navigate("overview");
          }}
        >
          <img src="/favicon.svg" alt="" />
          <span>nekoport</span>
          <small>邮件</small>
        </a>
        <div className="search-box">
          <Search size={17} />
          <input
            aria-label="搜索邮件"
            placeholder="搜索已同步邮件…"
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              if (e.target.value.trim() && !mailView) navigate("inbox");
            }}
          />
          {search && (
            <button
              className="icon-button"
              aria-label="清除搜索"
              onClick={() => setSearch("")}
            >
              <X size={14} />
            </button>
          )}
          <kbd>搜索</kbd>
        </div>
        <div className="topbar-actions">
          <span className={`sync-indicator ${working ? "active" : ""}`}>
            <RefreshCw size={13} className={working ? "spin" : ""} />
            <span>{working ? "正在同步" : "本机邮件服务"}</span>
          </span>
          <button
            className="icon-button"
            title="设置"
            aria-label="打开设置"
            onClick={() => navigate("settings")}
          >
            <Settings2 size={18} />
          </button>
        </div>
      </header>
      {navOpen && (
        <button
          className="sidebar-mask"
          aria-label="关闭导航"
          onClick={() => setNavOpen(false)}
        />
      )}
      <aside className={`sidebar ${navOpen ? "open" : ""}`}>
        <button className="primary compose-button" onClick={startCompose}>
          <Plus size={18} />
          写邮件
        </button>
        <nav className="main-nav" aria-label="邮件导航">
          {[
            {
              id: "overview",
              label: "邮件概览",
              icon: LayoutDashboard,
              count: 0,
            },
            {
              id: "inbox",
              label: "所有收件箱",
              icon: Inbox,
              count: unreadTotal,
            },
            { id: "starred", label: "星标邮件", icon: Star, count: 0 },
            {
              id: "drafts",
              label: "草稿",
              icon: FileText,
              count: data?.drafts.length || 0,
            },
            {
              id: "outbox",
              label: "发件箱",
              icon: Send,
              count:
                data?.jobs.filter(
                  (j) => !["sent", "cancelled"].includes(j.state),
                ).length || 0,
            },
          ].map((item) => (
            <button
              key={item.id}
              className={`nav-item ${view === item.id && !mailbox ? "selected" : ""}`}
              onClick={() => navigate(item.id)}
            >
              <item.icon size={16} />
              <span>{item.label}</span>
              {item.count > 0 && <small className="count">{item.count}</small>}
            </button>
          ))}
        </nav>
        <div className="section-label">
          我的邮箱 <span>{accounts.length}</span>
        </div>
        <div className="account-navigation">
          {accounts.map((a) => (
            <div className="account-group" key={a.id}>
              <button
                className={`nav-item account-title ${expandedAccount === a.id ? "selected" : ""}`}
                aria-expanded={expandedAccount === a.id}
                aria-controls={`account-folders-${a.id}`}
                onClick={() => {
                  setExpandedAccount((current) =>
                    current === a.id ? "" : a.id,
                  );
                }}
                title={a.address}
              >
                {expandedAccount !== a.id ? (
                  <ChevronRight size={13} />
                ) : (
                  <ChevronDown size={13} />
                )}
                <span
                  className={`account-dot ${a.error ? "bad" : a.paused ? "paused" : ""}`}
                />
                <span>{a.name}</span>
                {data?.running[a.id] && (
                  <RefreshCw size={12} className="spin" />
                )}
              </button>
              {expandedAccount === a.id && (
                <div className="folder-list" id={`account-folders-${a.id}`}>
                  {(data?.mailboxes || [])
                    .filter((b) => b.accountId === a.id)
                    .map((b) => {
                      const Icon =
                        b.role === "inbox"
                          ? Inbox
                          : b.role === "sent"
                            ? Send
                            : b.role === "trash"
                              ? Trash2
                              : b.role === "archive"
                                ? Archive
                                : Folder;
                      return (
                        <button
                          key={b.id}
                          className={`nav-item folder-item ${mailbox === b.id ? "selected" : ""}`}
                          title={b.name}
                          onClick={() => navigate("folder", b.id)}
                        >
                          <Icon size={14} />
                          <span>{labels[b.role] || b.name}</span>
                          {b.unread > 0 && <small>{b.unread}</small>}
                        </button>
                      );
                    })}
                  {!(data?.mailboxes || []).some(
                    (b) => b.accountId === a.id,
                  ) && (
                    <p className="folder-placeholder">
                      {a.error
                        ? "连接失败，请检查设置"
                        : a.paused
                          ? "同步已暂停"
                          : "等待获取文件夹…"}
                    </p>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
        <button className="add-account" onClick={() => setEditor(null)}>
          <Plus size={15} />
          添加邮箱
        </button>
        <div className="sidebar-bottom">
          <button
            className={`nav-item ${view === "settings" ? "selected" : ""}`}
            onClick={() => navigate("settings")}
          >
            <Settings2 size={16} />
            设置与同步
          </button>
          <div className="storage-note">
            <ShieldCheck size={13} />
            <span>个人邮件空间</span>
          </div>
        </div>
      </aside>
      <main className={`workspace ${selected ? "has-selection" : ""}`}>
        <div className="global-feedback">
          {data?.storage?.writable === false && (
            <div className="global-error" role="alert">
              <AlertCircle size={16} />
              <span>
                存储已进入只读保护，请检查磁盘并重启恢复；发送结果需核对后处理。
              </span>
            </div>
          )}
          {error && (
            <div className="global-error" role="alert">
              <AlertCircle size={16} />
              <span>{error}</span>
              <button
                className="icon-button"
                aria-label="关闭错误"
                onClick={() => setError("")}
              >
                <X size={14} />
              </button>
            </div>
          )}
          {(refreshError || (mailView && listError)) && (
            <div className="notice refresh-warning" role="status">
              <AlertCircle size={16} />
              <span>
                {refreshError
                  ? `页面数据暂时无法更新：${refreshError}`
                  : `邮件列表暂时无法更新：${listError}`}
                。已有内容仍可查看，刷新成功后提示会自动消失。
              </span>
              <button
                className="secondary small"
                onClick={() => void refresh()}
              >
                重试刷新
              </button>
              <button
                className="icon-button"
                aria-label="关闭刷新提示"
                onClick={() => {
                  clearRefreshError();
                  setListError("");
                }}
              >
                <X size={14} />
              </button>
            </div>
          )}
          {data?.operations.some((o) => o.state === "failed") &&
            view !== "settings" && (
              <div className="notice" role="status">
                后台邮件操作有失败，请查看操作记录；移动结果不确定时请先同步核对。
                <button
                  className="secondary small"
                  onClick={() => navigate("settings")}
                >
                  查看操作记录
                </button>
              </div>
            )}
          {notice && (
            <div className="notice" role="status">
              {notice}
            </div>
          )}
        </div>
        {view === "overview" ? (
          data ? (
            <Overview
              data={data}
              navigate={navigate}
              onAdd={() => setEditor(null)}
              onCompose={startCompose}
              onAccount={setEditor}
              onRefresh={refresh}
              onSync={() =>
                act(async () => {
                  await Promise.all(
                    accounts
                      .filter((a) => !a.paused)
                      .map((a) => api(`/accounts/${a.id}/sync`, "POST")),
                  );
                })
              }
            />
          ) : (
            <section className="full-panel">
              <PanelHeader title="邮件概览" sub="正在加载邮箱状态…" />
            </section>
          )
        ) : view === "settings" && data ? (
          <SettingsPage
            data={data}
            onAccount={setEditor}
            onSaved={refresh}
            onError={setError}
            onSync={(id) => void act(() => api(`/accounts/${id}/sync`, "POST"))}
            accountChangeBlocked={compose !== null}
            onReauthenticate={() => {
              setCSRF("");
              setAuth(false);
              setData(null);
              setNotice("账户已更新，请使用新的账号信息重新登录。");
            }}
            onDelete={(a) =>
              setConfirm({
                title: "移除邮箱",
                text: `移除“${a.name}”及本地草稿和缓存？远端邮件不会被删除。`,
                action: async () => {
                  await api(`/accounts/${a.id}`, "DELETE");
                  await refresh();
                },
              })
            }
            onLogout={() =>
              void act(async () => {
                await api("/logout", "POST");
                setAuth(false);
                setData(null);
              })
            }
          />
        ) : view === "drafts" ? (
          <section className="full-panel">
            <PanelHeader
              title="草稿"
              sub="本地自动保存的邮件，不与服务器草稿同步。"
            />
            <div className="record-list">
              {!data?.drafts.length ? (
                <Empty
                  title="还没有草稿"
                  description="未发送的邮件会保存在这里。"
                >
                  <button className="secondary" onClick={startCompose}>
                    写一封邮件
                  </button>
                </Empty>
              ) : (
                data.drafts.map((d) => (
                  <article className="record" key={d.id}>
                    <div className="record-icon">
                      <FileText size={20} />
                    </div>
                    <button
                      className="record-main"
                      onClick={() => void openDraft(d.id)}
                    >
                      <strong>{d.subject || "（无主题）"}</strong>
                      <span>{d.to || "尚未填写收件人"}</span>
                      <small>
                        {accounts.find((a) => a.id === d.accountId)?.name} ·{" "}
                        {time(d.updatedAt, true)}
                      </small>
                    </button>
                    <button
                      className="secondary small"
                      onClick={() => void openDraft(d.id)}
                    >
                      继续编辑
                    </button>
                    <button
                      className="icon-button"
                      aria-label="删除草稿"
                      onClick={() =>
                        setConfirm({
                          title: "删除草稿",
                          text: "删除这封本地草稿？",
                          action: async () => {
                            await api(`/drafts/${d.id}`, "DELETE");
                            await refresh();
                          },
                        })
                      }
                    >
                      <Trash2 size={16} />
                    </button>
                  </article>
                ))
              )}
            </div>
          </section>
        ) : view === "outbox" ? (
          <section className="full-panel">
            <PanelHeader
              title="发件箱"
              sub="发送状态持久保存，结果待确认的任务不会自动重发。"
            />
            <div className="record-list">
              {!data?.jobs.length ? (
                <Empty
                  title="发件箱为空"
                  description="你提交发送的邮件会在这里显示进度。"
                />
              ) : (
                data.jobs.map((j) => (
                  <article className="record job-record" key={j.id}>
                    <div className="record-icon">
                      <Send size={19} />
                    </div>
                    <div className="record-main">
                      <strong>
                        {j.subject || "（无主题）"}
                        <span
                          className={`badge ${j.state === "sent" ? "success" : j.state === "failed" || j.state === "unknown" ? "danger" : ""}`}
                        >
                          {jobNames[j.state] || j.state}
                        </span>
                      </strong>
                      <span>{j.toDisplay || j.to}</span>
                      <small>
                        {j.fromDisplay ||
                          accounts.find((a) => a.id === j.accountId)?.name}{" "}
                        · {time(j.createdAt, true)}
                      </small>
                      {j.error && <p className="danger">{j.error}</p>}
                      {j.state === "sent" && (
                        <small>
                          {j.archive === "saved"
                            ? "已保存到已发送文件夹"
                            : j.archive === "server"
                              ? "由服务器保存，尚未核实"
                              : j.archive === "confirmed"
                                ? "已由用户确认"
                                : "归档状态：" + j.archive}
                        </small>
                      )}
                    </div>
                    <div className="job-actions">
                      {j.state === "failed" && (
                        <button
                          className="secondary small"
                          onClick={() =>
                            void act(() =>
                              api(`/send-jobs/${j.id}/action`, "POST", {
                                action: "retry",
                              }),
                            )
                          }
                        >
                          重试发送
                        </button>
                      )}
                      {j.state === "sent" && j.archive === "failed" && (
                        <button
                          className="secondary small"
                          onClick={() =>
                            void act(() =>
                              api(`/send-jobs/${j.id}/action`, "POST", {
                                action: "archive",
                              }),
                            )
                          }
                        >
                          重试归档
                        </button>
                      )}
                      {j.state === "unknown" && (
                        <button
                          className="secondary small"
                          onClick={() =>
                            setConfirm({
                              title: "确认已经发送",
                              text: "请先在收件邮箱或已发送文件夹中核对。确认后任务将标记为已发送，不会再次投递。",
                              action: async () => {
                                await api(`/send-jobs/${j.id}/action`, "POST", {
                                  action: "confirm-sent",
                                });
                                await refresh();
                              },
                            })
                          }
                        >
                          确认已发送
                        </button>
                      )}
                      {["queued", "failed", "unknown"].includes(j.state) && (
                        <button
                          className="secondary small"
                          onClick={() =>
                            setConfirm({
                              title: "取消任务",
                              text:
                                j.state === "unknown"
                                  ? "取消此记录不会撤回可能已投递的邮件。核对后才考虑重新发送，避免重复。"
                                  : "取消尚未发送的任务？草稿将保留。",
                              action: async () => {
                                await api(`/send-jobs/${j.id}/action`, "POST", {
                                  action: "cancel",
                                });
                                await refresh();
                              },
                            })
                          }
                        >
                          取消任务
                        </button>
                      )}
                    </div>
                  </article>
                ))
              )}
            </div>
          </section>
        ) : (
          <div className="mail-workspace">
            <section className="message-panel">
              <div className="list-heading">
                <div>
                  <h1>{title}</h1>
                  <small className="muted">
                    {currentBox
                      ? accounts.find((a) => a.id === currentBox.accountId)
                          ?.address
                      : "把每一封来信，放在眼前"}
                  </small>
                </div>
                <button
                  className="icon-button"
                  title="同步所有邮箱"
                  aria-label="同步所有邮箱"
                  disabled={working || !accounts.some((a) => !a.paused)}
                  onClick={() => {
                    for (const a of accounts.filter((a) => !a.paused))
                      void act(() => api(`/accounts/${a.id}/sync`, "POST"));
                  }}
                >
                  <RefreshCw size={17} className={working ? "spin" : ""} />
                </button>
              </div>
              <div className="list-tabs">
                <button
                  className={!unread ? "active" : ""}
                  onClick={() => setUnread(false)}
                >
                  全部
                </button>
                <button
                  className={unread ? "active" : ""}
                  onClick={() => setUnread(true)}
                >
                  未读
                </button>
                <span className="spacer" />
                <button
                  className={`icon-button ${filters ? "active" : ""}`}
                  title="筛选"
                  aria-label="打开筛选"
                  onClick={() => setFilters(!filters)}
                >
                  <SlidersHorizontal size={15} />
                </button>
              </div>
              {filters && (
                <div className="filter-panel">
                  <Select
                    label="搜索范围"
                    value={accountScope}
                    onChange={setAccountScope}
                    options={[
                      { value: "", label: "所有邮箱" },
                      ...accounts.map((a) => ({ value: a.id, label: a.name })),
                    ]}
                  />
                  <label className="check">
                    <input
                      type="checkbox"
                      checked={attachments}
                      onChange={(e) => setAttachments(e.target.checked)}
                    />
                    仅含附件
                  </label>
                  <label className="check">
                    <input
                      type="checkbox"
                      checked={bodySearch}
                      onChange={(e) => setBodySearch(e.target.checked)}
                    />
                    包含已缓存正文
                  </label>
                  <div className="form-grid">
                    <Field label="开始日期">
                      <input
                        type="date"
                        value={after}
                        onChange={(e) => setAfter(e.target.value)}
                      />
                    </Field>
                    <Field label="结束日期">
                      <input
                        type="date"
                        value={before}
                        onChange={(e) => setBefore(e.target.value)}
                      />
                    </Field>
                  </div>
                </div>
              )}
              {checked.size > 0 && (
                <div className="bulk-bar">
                  <span>已选 {checked.size} 封</span>
                  <button
                    className="icon-button"
                    title="标为已读"
                    aria-label="批量标为已读"
                    onClick={() => void operation("seen", true)}
                  >
                    <CheckCheck size={16} />
                  </button>
                  <button
                    className="icon-button"
                    title="移动"
                    aria-label="批量移动"
                    onClick={() => {
                      setMoveTarget("");
                      setMove(true);
                    }}
                  >
                    <MoveRight size={16} />
                  </button>
                  <button
                    className="icon-button"
                    title="移到已删除"
                    aria-label="批量删除"
                    onClick={deleteMail}
                  >
                    <Trash2 size={16} />
                  </button>
                  <button
                    className="icon-button"
                    aria-label="取消多选"
                    onClick={() => setChecked(new Set())}
                  >
                    <X size={15} />
                  </button>
                </div>
              )}
              <div className="message-scroll">
                {listLoading && !items.length ? (
                  <div className="skeleton-list">
                    {[1, 2, 3, 4, 5].map((n) => (
                      <div className="skeleton" key={n} />
                    ))}
                  </div>
                ) : listError && !items.length ? (
                  <Empty title="暂时无法加载邮件列表" description={listError}>
                    <button
                      className="secondary"
                      onClick={() => void refresh()}
                    >
                      重新加载邮件
                    </button>
                  </Empty>
                ) : !items.length ? (
                  <Empty
                    title={
                      !accounts.length
                        ? "连接你的第一个邮箱"
                        : query || unread || attachments
                          ? "没有符合条件的邮件"
                          : "这里还没有邮件"
                    }
                    description={
                      !accounts.length
                        ? "手动设置 IMAP 与 SMTP，开始收发邮件。"
                        : query
                          ? "试试其他关键词，或调整搜索范围。"
                          : working
                            ? "正在获取邮件，请稍候。"
                            : "同步后，邮件会出现在这里。"
                    }
                  >
                    {!accounts.length && (
                      <button
                        className="secondary"
                        onClick={() => setEditor(null)}
                      >
                        <Plus size={14} />
                        添加邮箱
                      </button>
                    )}
                  </Empty>
                ) : (
                  items.map((m) => (
                    <article
                      key={m.id}
                      className={`message-row ${m.seen ? "" : "unread"} ${selected === m.id ? "selected" : ""}`}
                    >
                      <div className="row-check">
                        <input
                          type="checkbox"
                          aria-label={`选择 ${m.subject || "无主题邮件"}`}
                          checked={checked.has(m.id)}
                          onChange={(e) => {
                            const next = new Set(checked);
                            e.target.checked
                              ? next.add(m.id)
                              : next.delete(m.id);
                            setChecked(next);
                          }}
                        />
                      </div>
                      <button
                        className="message-summary"
                        onClick={() => setSelected(m.id)}
                      >
                        <div className="message-row-top">
                          <span className="sender">
                            {m.fromName || m.fromDisplay || m.from}
                          </span>
                          <time>{time(m.date)}</time>
                        </div>
                        <div className="subject-line">
                          {!m.seen && <span className="unread-dot" />}
                          <span>{m.subject || "（无主题）"}</span>
                          {m.hasAttachments && <Paperclip size={12} />}
                        </div>
                        <p>
                          {m.preview || m.toDisplay || m.to || "打开以读取正文"}
                        </p>
                        <div className="row-meta">
                          <span className="account-tag">
                            {accounts.find((a) => a.id === m.accountId)?.name}
                          </span>
                        </div>
                      </button>
                      <button
                        className={`row-star icon-button ${m.starred ? "starred" : ""}`}
                        aria-label={m.starred ? "取消星标" : "添加星标"}
                        onClick={() =>
                          void operation("starred", !m.starred, "", [m.id])
                        }
                      >
                        <Star
                          size={14}
                          fill={m.starred ? "currentColor" : "none"}
                        />
                      </button>
                    </article>
                  ))
                )}
                {cursor && (
                  <button
                    className="load-more secondary"
                    disabled={listLoading}
                    onClick={() => void more()}
                  >
                    {listLoading
                      ? "加载中…"
                      : partial
                        ? "继续搜索"
                        : "加载更多"}
                  </button>
                )}
                {partial && (
                  <p className="search-note">
                    本次扫描已到时间预算，可继续搜索。
                  </p>
                )}
              </div>
              <div className="list-bottom">
                <span>
                  {items.length} 封{cursor ? " · 可继续加载" : ""}
                </span>
                <span>{query ? "已同步邮件搜索" : "本地缓存"}</span>
              </div>
            </section>
            <div
              role="separator"
              tabIndex={0}
              aria-label="调整邮件列表宽度"
              aria-orientation="vertical"
              className="column-resizer"
              onKeyDown={(e) => {
                if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                  const width = Math.max(
                    300,
                    Math.min(
                      520,
                      listWidth + (e.key === "ArrowRight" ? 10 : -10),
                    ),
                  );
                  setListWidth(width);
                  localStorage.setItem("nekoport-list-width", String(width));
                }
              }}
              onPointerDown={(e) => {
                const start = e.clientX,
                  width = listWidth;
                const move = (v: PointerEvent) =>
                  setListWidth(
                    Math.max(300, Math.min(520, width + v.clientX - start)),
                  );
                const up = (v: PointerEvent) => {
                  localStorage.setItem(
                    "nekoport-list-width",
                    String(
                      Math.max(300, Math.min(520, width + v.clientX - start)),
                    ),
                  );
                  window.removeEventListener("pointermove", move);
                  window.removeEventListener("pointerup", up);
                };
                window.addEventListener("pointermove", move);
                window.addEventListener("pointerup", up);
              }}
            />
            <section className="reader-panel">
              <div className="reader-toolbar">
                <button
                  className="icon-button mobile-back"
                  aria-label="返回邮件列表"
                  onClick={() => setSelected("")}
                >
                  <ChevronLeft size={19} />
                </button>
                {selected ? (
                  <>
                    <button
                      className="secondary small"
                      disabled={!detail}
                      onClick={() => reply("reply")}
                    >
                      <Reply size={15} />
                      回复
                    </button>
                    <button
                      className="secondary small"
                      disabled={!detail}
                      onClick={() => reply("all")}
                    >
                      <ReplyAll size={15} />
                      <span>全部回复</span>
                    </button>
                    <button
                      className="secondary small"
                      disabled={!detail}
                      onClick={() => reply("forward")}
                    >
                      <Forward size={15} />
                      转发
                    </button>
                    <span className="spacer" />
                    <button
                      className="icon-button"
                      aria-label="归档邮件"
                      title={
                        alreadyArchived
                          ? "邮件已在归档文件夹"
                          : !archiveBox
                            ? "未找到归档文件夹，请先同步邮箱或使用移动操作"
                            : archiveBusy
                              ? "正在提交归档请求"
                              : "归档"
                      }
                      disabled={
                        !selectedMessage ||
                        !archiveBox ||
                        alreadyArchived ||
                        archiveBusy
                      }
                      onClick={() => void archiveMail()}
                    >
                      <Archive size={16} />
                    </button>
                    <button
                      className="icon-button"
                      title="移动"
                      aria-label="移动邮件"
                      onClick={() => {
                        setMoveTarget("");
                        setMove(true);
                      }}
                    >
                      <MoveRight size={16} />
                    </button>
                    <button
                      className="icon-button"
                      title={selectedMessage?.seen ? "标为未读" : "标为已读"}
                      aria-label="切换已读状态"
                      onClick={() =>
                        void operation("seen", !selectedMessage?.seen, "", [
                          selected,
                        ])
                      }
                    >
                      <Mail size={16} />
                    </button>
                    <button
                      className="icon-button danger-hover"
                      aria-label="移到已删除"
                      title="移到已删除"
                      onClick={deleteMail}
                    >
                      <Trash2 size={16} />
                    </button>
                  </>
                ) : (
                  <span className="muted">邮件阅读</span>
                )}
              </div>
              {!selected ? (
                <Empty
                  title="留一点空间，读一封邮件"
                  description="从左侧选择邮件，在这里查看正文和附件。"
                >
                  <div className="reader-hint">
                    <ShieldCheck size={14} />
                    远程图片默认拦截
                  </div>
                </Empty>
              ) : detailLoading ? (
                <div className="reader-loading">
                  <RefreshCw className="spin" size={20} />
                  <p>正在读取邮件…</p>
                  <small>未缓存的正文需要从邮箱服务器下载</small>
                </div>
              ) : detailError ? (
                <Empty title="暂时无法读取" description={detailError}>
                  <button
                    className="secondary"
                    onClick={() => {
                      const id = selected;
                      setSelected("");
                      setTimeout(() => setSelected(id), 0);
                    }}
                  >
                    重试
                  </button>
                </Empty>
              ) : detail ? (
                <div className="reader-scroll">
                  <header className="message-header">
                    <div className="message-eyebrow">
                      {
                        accounts.find((a) => a.id === detail.message.accountId)
                          ?.name
                      }
                      <span>{time(detail.message.date, true)}</span>
                    </div>
                    <h1>{detail.message.subject || "（无主题）"}</h1>
                    <div className="sender-block">
                      <div className="avatar">
                        {(
                          Array.from(
                            detail.message.fromName || detail.message.from,
                          )[0] || "邮"
                        ).toUpperCase()}
                      </div>
                      <div>
                        <strong>
                          {detail.message.fromDisplay || detail.message.from}
                        </strong>
                        <p>
                          收件人：
                          {detail.message.toDisplay || detail.message.to}
                        </p>
                        {detail.message.cc && (
                          <p>
                            抄送：
                            {detail.message.ccDisplay || detail.message.cc}
                          </p>
                        )}
                      </div>
                    </div>
                    <div className="reading-options">
                      {detail.content.html && (
                        <button
                          className="secondary small"
                          onClick={() => setPlain(!plain)}
                        >
                          <Eye size={13} />
                          {plain ? "查看 HTML" : "查看纯文本"}
                        </button>
                      )}
                      <a
                        className="secondary small button"
                        href={download(`/messages/${selected}/raw`)}
                      >
                        <ArrowDownToLine size={13} />
                        原始邮件
                      </a>
                    </div>
                  </header>
                  {detail.content.html && !plain ? (
                    <>
                      <div className="privacy-banner">
                        <ShieldCheck size={14} />
                        <span>
                          {remote
                            ? "本封邮件允许加载远程图片"
                            : "外部图片已拦截，保护阅读隐私"}
                        </span>
                        <button
                          className="secondary small"
                          onClick={() => setRemote(!remote)}
                        >
                          {remote ? "重新拦截" : "本封允许图片"}
                        </button>
                      </div>
                      <MailFrame html={frameHTML} />
                    </>
                  ) : (
                    <pre
                      className="mail-text"
                      style={{ fontSize: settings?.fontSize || 15 }}
                    >
                      {detail.content.text || "（邮件没有文本正文）"}
                    </pre>
                  )}
                  {detail.content.attachments.length > 0 && (
                    <section className="message-attachments">
                      <h3>
                        <Paperclip size={15} />
                        {detail.content.attachments.length} 个附件
                      </h3>
                      <div className="attachment-list">
                        {detail.content.attachments.map((at) => (
                          <a
                            className="attachment downloadable"
                            key={at.id}
                            href={download(
                              `/messages/${selected}/attachments/${at.id}`,
                            )}
                          >
                            <FileText size={22} />
                            <span>
                              <strong>{at.name}</strong>
                              <small>{size(at.size)} · 下载附件</small>
                            </span>
                            <ArrowDownToLine size={15} />
                          </a>
                        ))}
                      </div>
                    </section>
                  )}
                  <div className="reader-footer">
                    <button
                      className="secondary"
                      onClick={() => reply("reply")}
                    >
                      <Reply size={15} />
                      回复
                    </button>
                    <button
                      className="secondary"
                      onClick={() => reply("forward")}
                    >
                      <Forward size={15} />
                      转发
                    </button>
                  </div>
                </div>
              ) : null}
            </section>
          </div>
        )}
      </main>
      {editor !== undefined && (
        <AccountEditor
          account={editor || undefined}
          onClose={() => setEditor(undefined)}
          onSaved={() => void refresh()}
        />
      )}{" "}
      {compose && (
        <Compose
          initial={compose}
          accounts={accounts}
          onClose={() => setCompose(null)}
          onSaved={() => void refresh()}
        />
      )}{" "}
      {confirm && (
        <Modal
          title={confirm.title}
          onClose={() => {
            if (!confirmBusy) setConfirm(null);
          }}
        >
          <p className="confirm-message">{confirm.text}</p>
          <footer className="modal-footer">
            <button
              className="secondary"
              disabled={confirmBusy}
              onClick={() => setConfirm(null)}
            >
              取消
            </button>
            <button
              className="primary"
              disabled={confirmBusy}
              onClick={async () => {
                setConfirmBusy(true);
                try {
                  await confirm.action();
                  setConfirm(null);
                } catch (e) {
                  setError((e as Error).message);
                  setConfirm(null);
                } finally {
                  setConfirmBusy(false);
                }
              }}
            >
              {confirmBusy ? "处理中…" : "确认"}
            </button>
          </footer>
        </Modal>
      )}{" "}
      {move && (
        <Modal title="移动到文件夹" onClose={() => setMove(false)}>
          <div className="modal-fields">
            <Select
              label="目标文件夹"
              value={moveTarget}
              onChange={setMoveTarget}
              options={[
                { value: "", label: "请选择目标文件夹" },
                ...(data?.mailboxes || [])
                  .filter(
                    (b) =>
                      b.accountId ===
                      (checked.size
                        ? items.find((m) => checked.has(m.id))?.accountId
                        : detail?.message.accountId),
                  )
                  .map((b) => ({
                    value: b.id,
                    label: labels[b.role] || b.name,
                  })),
              ]}
            />
            <p className="muted">
              只能移至同一邮箱，服务器必须支持安全 UID MOVE。
            </p>
          </div>
          <footer className="modal-footer">
            <button className="secondary" onClick={() => setMove(false)}>
              取消
            </button>
            <button
              className="primary"
              disabled={!moveTarget}
              onClick={() => {
                void operation("move", false, moveTarget);
                setMove(false);
              }}
            >
              移动
            </button>
          </footer>
        </Modal>
      )}
    </div>
  );
}
function PanelHeader({ title, sub }: { title: string; sub: string }) {
  return (
    <header className="panel-header">
      <h1>{title}</h1>
      <p className="muted">{sub}</p>
    </header>
  );
}
function Login({
  onLogin,
  error: initial,
  notice,
}: {
  onLogin: () => void;
  error: string;
  notice?: string;
}) {
  const [username, setUsername] = useState(""),
    [password, setPassword] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(initial);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const v = await api<{ csrf: string }>("/login", "POST", {
        username,
        password,
      });
      setCSRF(v.csrf);
      onLogin();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="login-layout">
      <section className="login-story" aria-label="nekoport 邮件客户端">
        <div className="login-brand">
          <img src="/favicon.svg" alt="" width={35} height={35} />
          <span>nekoport</span>
          <span className="login-version">个人邮件客户端</span>
        </div>
        <div className="login-intro">
          <span className="login-eyebrow">让每一封邮件，井然有序。</span>
          <h1>
            所有来信，
            <br />
            一处管理<span>。</span>
          </h1>
          <p>
            从日常沟通到重要消息，从收件阅读到回复发送。
            <br />
            让你的邮件回归清晰、有序。
          </p>
          <div className="login-features">
            <span>
              <Mail size={15} />
              多邮箱管理
            </span>
            <span>
              <ShieldCheck size={15} />
              安全阅读
            </span>
            <span>
              <Send size={15} />
              便捷收发
            </span>
          </div>
        </div>
        <small>nekoport / 个人使用 · 自主管理</small>
      </section>
      <section className="login-panel" aria-labelledby="login-title">
        <form onSubmit={submit}>
          <span className="login-eyebrow">邮件客户端登录</span>
          <h2 id="login-title">欢迎回来</h2>
          <p>登录以查看和管理你的邮件。</p>
          <Field label="用户名">
            <input
              required
              disabled={busy}
              autoComplete="username"
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </Field>
          <Field label="密码">
            <input
              required
              disabled={busy}
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          <ErrorBox error={error} />
          {notice && (
            <p className="success" role="status">
              {notice}
            </p>
          )}
          <button className="primary" disabled={busy}>
            {busy ? "正在登录…" : "进入邮件空间"}
          </button>
          <div className="login-note">
            <ShieldCheck size={14} />
            <span>
              首次启动生成的默认账号和密码可在服务控制台查看，登录后可添加并管理邮箱。
            </span>
          </div>
        </form>
      </section>
    </main>
  );
}
function SettingsPage({
  data,
  onAccount,
  onSaved,
  onError,
  onSync,
  onDelete,
  onLogout,
  onReauthenticate,
  accountChangeBlocked,
}: {
  data: Bootstrap;
  onAccount: (a: Account | null) => void;
  onSaved: () => Promise<void>;
  onError: (e: string) => void;
  onSync: (id: string) => void;
  onDelete: (a: Account) => void;
  onLogout: () => void;
  onReauthenticate: () => void;
  accountChangeBlocked: boolean;
}) {
  const [value, setValue] = useState<Settings>(data.settings),
    [busy, setBusy] = useState(false),
    [syncAccountID, setSyncAccountID] = useState(""),
    [syncBusy, setSyncBusy] = useState(false);
  const syncAccount = data.accounts.find((a) => a.id === syncAccountID);
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await api("/settings", "PUT", value);
      await onSaved();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="settings-page">
      <PanelHeader
        title="设置与同步"
        sub="管理你的邮箱连接、阅读偏好和同步任务。"
      />
      <div className="settings-content">
        <div className="settings-section-heading">
          <h2>
            <Server size={17} />
            我的邮箱
          </h2>
          <button className="secondary" onClick={() => onAccount(null)}>
            <Plus size={15} />
            添加邮箱
          </button>
        </div>
        <div className="account-cards">
          {!data.accounts.length && (
            <Empty
              title="尚未添加邮箱"
              description="所有服务器信息由你自主填写。"
            />
          )}
          {data.accounts.map((a) => (
            <article className="account-card" key={a.id}>
              <header>
                <div>
                  <h3>
                    {a.name}
                    <span className={`badge ${a.error ? "danger" : ""}`}>
                      {a.paused
                        ? "已暂停"
                        : data.running[a.id]
                          ? "同步中"
                          : a.error
                            ? "连接异常"
                            : "已配置"}
                    </span>
                  </h3>
                  <p>{a.address}</p>
                </div>
                <button
                  className="icon-button"
                  title="同步"
                  aria-label={`同步 ${a.name}`}
                  disabled={data.running[a.id]}
                  onClick={() => onSync(a.id)}
                >
                  <RefreshCw
                    size={16}
                    className={data.running[a.id] ? "spin" : ""}
                  />
                </button>
              </header>
              <dl>
                <div>
                  <dt>IMAP</dt>
                  <dd>
                    {a.imap.host}:{a.imap.port}
                  </dd>
                </div>
                <div>
                  <dt>SMTP</dt>
                  <dd>
                    {a.smtp.host}:{a.smtp.port}
                  </dd>
                </div>
                <div>
                  <dt>最近同步</dt>
                  <dd>{time(a.lastSync || "", true)}</dd>
                </div>
                <div>
                  <dt>同步范围</dt>
                  <dd>
                    {a.syncScope === "all"
                      ? "全部历史"
                      : `最近 ${a.syncDays} 天`}
                  </dd>
                </div>
              </dl>
              {a.error && <p className="form-error">{a.error}</p>}
              <footer>
                <button
                  className="secondary small"
                  onClick={() => onAccount(a)}
                >
                  编辑连接
                </button>
                <button
                  className="secondary small"
                  onClick={() => setSyncAccountID(a.id)}
                >
                  同步设置
                </button>
                <button
                  className="secondary small danger"
                  onClick={() => onDelete(a)}
                >
                  移除邮箱
                </button>
              </footer>
            </article>
          ))}
        </div>
        {syncAccount && (
          <Modal
            title="同步设置"
            description={`${syncAccount.name} · ${syncAccount.address}`}
            onClose={() => {
              if (!syncBusy) setSyncAccountID("");
            }}
          >
            <SyncPanel
              key={`${syncAccount.id}:${syncAccount.revision}`}
              account={syncAccount}
              status={data.syncStatus?.[syncAccount.id]}
              idle={data.idleStatus?.[syncAccount.id]}
              onSaved={onSaved}
              onBusyChange={setSyncBusy}
            />
            <footer className="modal-footer">
              <button
                className="secondary"
                disabled={syncBusy}
                onClick={() => setSyncAccountID("")}
              >
                关闭
              </button>
            </footer>
          </Modal>
        )}
        {data.authAccount && (
          <AccountSecurity
            key={data.authAccount.revision}
            account={data.authAccount}
            blocked={accountChangeBlocked}
            onSuccess={onReauthenticate}
          />
        )}
        <section className="preference-section">
          <h2>
            <Eye size={17} />
            阅读与缓存
          </h2>
          <form onSubmit={save}>
            <div className="form-grid">
              <Field label="阅读字号（12–24）">
                <input
                  required
                  inputMode="numeric"
                  pattern="[0-9]+"
                  value={value.fontSize}
                  onChange={(e) =>
                    setValue({ ...value, fontSize: Number(e.target.value) })
                  }
                />
              </Field>
              <Field label="普通可重取缓存预算（MiB）">
                <input
                  required
                  inputMode="numeric"
                  pattern="[0-9]+"
                  value={value.cacheMiB}
                  onChange={(e) =>
                    setValue({ ...value, cacheMiB: Number(e.target.value) })
                  }
                />
              </Field>
            </div>
            <p className="muted">
              增量同步通过 IMAP IDLE 实时监听，断线自动重连，无需设置轮询间隔。
            </p>
            <label className="check">
              <input
                type="checkbox"
                checked={value.autoRead}
                onChange={(e) =>
                  setValue({ ...value, autoRead: e.target.checked })
                }
              />
              打开邮件正文后自动标为已读
            </label>
            <p className="muted">
              远程图片默认拦截，可在每封邮件中单独允许。正文与附件按需读取，草稿和待发送内容受保护。
            </p>
            <button className="secondary" disabled={busy}>
              {busy ? "保存中…" : "保存偏好"}
            </button>
          </form>
        </section>
        <section className="preference-section">
          <h2>
            <Clock size={17} />
            未完成的邮件操作
          </h2>
          <p className="muted">
            移动、删除先在本地移出列表，后台再提交；目标目录等待服务器确认。失败时恢复显示；结果不确定的移动请先同步核对，不会自动重放。
          </p>
          {data.operations.length ? (
            data.operations.map((o) => (
              <div className="operation-row" key={o.id}>
                <div>
                  <strong>
                    {o.kind === "seen"
                      ? "已读状态"
                      : o.kind === "starred"
                        ? "星标状态"
                        : "移动邮件"}{" "}
                    ·{" "}
                    {o.state === "queued"
                      ? "等待同步"
                      : o.state === "running"
                        ? "执行中"
                        : "失败"}
                  </strong>
                  <p className="danger">{o.error}</p>
                </div>
                {o.state === "failed" &&
                  ["seen", "starred"].includes(o.kind) && (
                    <button
                      className="secondary small"
                      onClick={() =>
                        void api(`/operations/${o.id}/retry`, "POST")
                          .then(onSaved)
                          .catch((e) => onError(e.message))
                      }
                    >
                      重试
                    </button>
                  )}
              </div>
            ))
          ) : (
            <p className="muted">没有未完成的操作。</p>
          )}
        </section>
        <section className="preference-section about">
          <div>
            <h2>nekoport</h2>
            <p className="muted">个人使用 · Go + React · JSON / JSONL</p>
            <small className="muted">
              已同步 {data.total} 封邮件 · IMAP IDLE 实时监听 ·
              按邮箱计划完整对齐
            </small>
          </div>
          <button className="secondary" onClick={onLogout}>
            <LogOut size={15} />
            退出登录
          </button>
        </section>
      </div>
    </section>
  );
}
createRoot(document.getElementById("root")!).render(<App />);

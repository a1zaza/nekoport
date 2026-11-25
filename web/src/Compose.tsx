import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  Send,
  Paperclip,
  Trash2,
  FileText,
  Maximize2,
  Minimize2,
} from "lucide-react";
import { api, size, download } from "./api";
import { Modal, Field, Select, ErrorBox } from "./components";
import type { Account, Draft } from "./types";
export type ComposeValue = { draft: Draft; body: string };
export function Compose({
  initial,
  accounts,
  onClose,
  onSaved,
}: {
  initial: ComposeValue;
  accounts: Account[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [value, setValue] = useState(initial),
    [status, setStatus] = useState(initial.draft.id ? "已保存" : "尚未保存"),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [expanded, setExpanded] = useState(false),
    [confirm, setConfirm] = useState("");
  const latest = useRef(value);
  latest.current = value;
  const changes = useRef(0),
    saved = useRef(0),
    pending = useRef<Promise<Draft> | null>(null),
    file = useRef<HTMLInputElement>(null);
  // getRandomValues is also available on plain HTTP LAN origins. Generate
  // once per composer so retries keep the same idempotency key.
  const [sendKey] = useState(() => {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join(
      "",
    );
  });
  const composeRef = useRef<HTMLElement>(null);
  const mounted = useRef(true);
  const previousFocus = useRef(document.activeElement as HTMLElement | null);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      previousFocus.current?.focus();
    };
  }, []);
  function edit(p: Partial<Draft>, body?: string) {
    changes.current++;
    setStatus("尚未保存");
    setValue((v) => ({
      draft: { ...v.draft, ...p },
      body: body === undefined ? v.body : body,
    }));
  }
  async function save(): Promise<Draft> {
    if (pending.current) {
      await pending.current;
      if (saved.current === changes.current && latest.current.draft.id)
        return latest.current.draft;
      return save();
    }
    const work = async () => {
      const snapshot = latest.current,
        n = changes.current;
      setStatus("正在保存…");
      setError("");
      try {
        const d = await api<Draft>(
          snapshot.draft.id ? `/drafts/${snapshot.draft.id}` : "/drafts",
          snapshot.draft.id ? "PUT" : "POST",
          { ...snapshot.draft, body: snapshot.body },
        );
        saved.current = n;
        const next = {
          ...latest.current,
          draft: {
            ...latest.current.draft,
            id: d.id,
            revision: d.revision,
            bodyId: d.bodyId,
            attachments: d.attachments,
            updatedAt: d.updatedAt,
          },
        };
        latest.current = next;
        if (mounted.current) {
          setValue(next);
          setStatus(n === changes.current ? "已保存" : "尚未保存");
        }
        onSaved();
        return d;
      } catch (e) {
        if (mounted.current) {
          setError((e as Error).message);
          setStatus("保存失败");
        }
        throw e;
      }
    };
    pending.current = work();
    try {
      return await pending.current;
    } finally {
      pending.current = null;
    }
  }
  useEffect(() => {
    if (busy || saved.current === changes.current) return;
    const t = setTimeout(() => {
      void save().catch(() => {});
    }, 800);
    return () => clearTimeout(t);
  }, [value, busy]);
  useEffect(() => {
    const before = (e: BeforeUnloadEvent) => {
      if (changes.current !== saved.current) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", before);
    return () => window.removeEventListener("beforeunload", before);
  }, []);
  async function close() {
    if (busy) return;
    try {
      if (changes.current !== saved.current) await save();
      onClose();
    } catch {}
  }
  async function send(e?: FormEvent) {
    e?.preventDefault();
    if (!value.draft.subject && !confirm) {
      setConfirm("这封邮件没有主题，仍然发送吗？");
      return;
    }
    setConfirm("");
    setBusy(true);
    setError("");
    try {
      const d = await save();
      await api(`/drafts/${d.id}/send`, "POST", {
        key: sendKey,
        revision: d.revision,
      });
      onSaved();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function upload(files: FileList | null) {
    if (!files?.length) return;
    setBusy(true);
    setError("");
    try {
      let d = await save();
      for (const f of Array.from(files)) {
        if (f.size > 20 * 1024 * 1024) throw new Error("单附件不能超过 20 MiB");
        const data = new FormData();
        data.append("file", f);
        d = await api<Draft>(`/drafts/${d.id}/attachments`, "POST", data);
        latest.current = {
          ...latest.current,
          draft: {
            ...latest.current.draft,
            attachments: d.attachments,
            revision: d.revision,
          },
        };
        setValue(latest.current);
      }
      onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      if (file.current) file.current.value = "";
    }
  }
  async function remove(id: string) {
    setBusy(true);
    try {
      const d = await save();
      const next = await api<Draft>(
        `/drafts/${d.id}/attachments/${id}`,
        "DELETE",
      );
      latest.current = {
        ...latest.current,
        draft: {
          ...latest.current.draft,
          attachments: next.attachments,
          revision: next.revision,
        },
      };
      setValue(latest.current);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className={`compose-mask ${expanded ? "expanded" : ""}`}>
      <section
        className="compose"
        ref={composeRef}
        role="dialog"
        aria-modal="true"
        aria-label="写邮件"
        onKeyDown={(e) => {
          if (e.key === "Tab") {
            const nodes = Array.from(
              composeRef.current?.querySelectorAll<HTMLElement>(
                "button:not(:disabled),input:not(:disabled):not([hidden]),textarea:not(:disabled),a[href]",
              ) || [],
            );
            const first = nodes[0],
              last = nodes[nodes.length - 1];
            if (e.shiftKey && document.activeElement === first) {
              e.preventDefault();
              last?.focus();
            } else if (!e.shiftKey && document.activeElement === last) {
              e.preventDefault();
              first?.focus();
            }
          }
          if (e.key === "Escape") void close();
          if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
            e.preventDefault();
            if (!busy) void send();
          }
        }}
      >
        <header className="compose-header">
          <div>
            <FileText size={17} />
            <h2>{value.draft.inReplyTo ? "回复邮件" : "写邮件"}</h2>
            <small className="muted" role="status">
              {status}
            </small>
          </div>
          <div>
            <button
              className="icon-button"
              aria-label={expanded ? "还原写信窗口" : "最大化写信窗口"}
              onClick={() => setExpanded(!expanded)}
            >
              {expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
            </button>
            <button
              className="secondary small"
              disabled={busy}
              onClick={() => void close()}
            >
              保存并关闭
            </button>
          </div>
        </header>
        <form onSubmit={send}>
          <fieldset disabled={busy}>
            <div className="compose-fields">
              <Field label="发件邮箱">
                <Select
                  label="发件邮箱"
                  value={value.draft.accountId}
                  onChange={(v) => edit({ accountId: v })}
                  options={accounts.map((a) => ({
                    value: a.id,
                    label: `${a.name} <${a.address}>`,
                  }))}
                />
              </Field>
              <Field label="收件人">
                <input
                  autoFocus
                  value={value.draft.to}
                  onChange={(e) => edit({ to: e.target.value })}
                  placeholder="多个地址使用英文逗号分隔"
                  required
                />
              </Field>
              <div className="form-grid">
                <Field label="抄送">
                  <input
                    value={value.draft.cc}
                    onChange={(e) => edit({ cc: e.target.value })}
                  />
                </Field>
                <Field label="密送">
                  <input
                    value={value.draft.bcc}
                    onChange={(e) => edit({ bcc: e.target.value })}
                  />
                </Field>
              </div>
              <Field label="主题">
                <input
                  value={value.draft.subject}
                  maxLength={2048}
                  onChange={(e) => edit({ subject: e.target.value })}
                  placeholder="邮件主题"
                />
              </Field>
            </div>
            <textarea
              className="compose-body"
              aria-label="纯文本正文"
              value={value.body}
              onChange={(e) => edit({}, e.target.value)}
              placeholder="在这里写下邮件正文…"
            />
            <div className="attachment-list">
              {value.draft.attachments.map((at) => (
                <div className="attachment" key={at.id}>
                  <Paperclip size={14} />
                  <a
                    href={download(
                      `/drafts/${value.draft.id}/attachments/${at.id}`,
                    )}
                  >
                    {at.name}
                  </a>
                  <small>{size(at.size)}</small>
                  <button
                    type="button"
                    className="icon-button"
                    aria-label={`移除附件 ${at.name}`}
                    onClick={() => void remove(at.id)}
                  >
                    <Trash2 size={13} />
                  </button>
                </div>
              ))}
            </div>
          </fieldset>
          <div className="compose-feedback">
            <ErrorBox error={error} />
          </div>
          <footer className="compose-footer">
            <input
              type="file"
              ref={file}
              multiple
              hidden
              onChange={(e) => void upload(e.target.files)}
            />
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => file.current?.click()}
            >
              <Paperclip size={16} />
              添加附件
            </button>
            <small className="muted">纯文本 · 自动保存</small>
            <span className="spacer" />
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => void save().catch(() => {})}
            >
              保存草稿
            </button>
            <button className="primary" disabled={busy}>
              <Send size={15} />
              {busy ? "处理中…" : "发送"}
            </button>
          </footer>
        </form>
      </section>
      {confirm && (
        <Modal title="发送确认" onClose={() => setConfirm("")}>
          <p className="confirm-message">{confirm}</p>
          <footer className="modal-footer">
            <button className="secondary" onClick={() => setConfirm("")}>
              取消
            </button>
            <button className="primary" onClick={() => void send()}>
              仍然发送
            </button>
          </footer>
        </Modal>
      )}
    </div>
  );
}

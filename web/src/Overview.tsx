import { useState } from "react";
import {
  ArrowRight,
  Inbox,
  Mail,
  Server,
  FileText,
  RefreshCw,
  AlertCircle,
  Send,
  Plus,
  Settings2,
  Clock,
} from "lucide-react";
import { time } from "./api";
import type { Bootstrap, Account } from "./types";

export function Overview({
  data,
  navigate,
  onAdd,
  onCompose,
  onSync,
  onRefresh,
  onAccount,
}: {
  data: Bootstrap;
  navigate: (view: string, box?: string, account?: string) => void;
  onAdd: () => void;
  onCompose: () => void;
  onSync: () => Promise<void>;
  onRefresh: () => Promise<void>;
  onAccount: (account: Account) => void;
}) {
  const [busy, setBusy] = useState(false);
  const unread = data.mailboxes
    .filter((b) => b.role === "inbox")
    .reduce((n, b) => n + b.unread, 0);
  const syncing = data.accounts.filter((a) => data.running[a.id]).length;
  const errors = data.accounts.filter((a) => a.error);
  const paused = data.accounts.filter((a) => a.paused).length;
  const pendingJobs = data.jobs.filter(
    (j) => !["sent", "cancelled"].includes(j.state),
  );
  const failedOps = data.operations.filter((o) =>
    ["failed", "unknown"].includes(o.state),
  );
  const stats = [
    {
      label: "已配置邮箱",
      value: data.accounts.length,
      hint: `${syncing} 个同步中 · ${paused} 个已暂停`,
      icon: Server,
      view: "settings",
    },
    {
      label: "已同步邮件",
      value: data.total,
      hint: "所有文件夹的本地邮件索引",
      icon: Mail,
      view: "inbox",
    },
    {
      label: "未读收件",
      value: unread,
      hint: "所有收件箱中的未读邮件",
      icon: Inbox,
      view: "inbox",
    },
    {
      label: "本地草稿",
      value: data.drafts.length,
      hint: "尚未发送的本地草稿",
      icon: FileText,
      view: "drafts",
    },
  ];
  async function run(action: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    try {
      await action();
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="overview-page" aria-label="邮件概览">
      <header className="overview-heading">
        <div>
          <small>邮件状态 / 个人空间</small>
          <h1>邮件概览</h1>
          <p>邮箱连接、来信与待办，一目了然。</p>
        </div>
        <button
          className="secondary"
          disabled={busy}
          onClick={() => void run(onRefresh)}
        >
          <RefreshCw size={14} className={busy ? "spin" : ""} />
          刷新状态
        </button>
      </header>
      <div className="overview-banner">
        <span className="overview-banner-icon">
          <Mail size={25} strokeWidth={1.5} />
        </span>
        <div>
          <h2>
            {data.accounts.length
              ? "你的每一封来信，都在这里。"
              : "从第一个邮箱开始，连接你的来信。"}
          </h2>
          <p>
            {data.accounts.length
              ? `收件箱有 ${unread} 封未读邮件，随时继续你的阅读与写作。`
              : "手动配置邮箱连接，在一个地方管理邮件。"}
          </p>
        </div>
        <button
          className="secondary"
          onClick={() => (data.accounts.length ? navigate("inbox") : onAdd())}
        >
          {data.accounts.length ? "查看收件箱" : "添加第一个邮箱"}
          <ArrowRight size={14} />
        </button>
      </div>
      <div className="overview-stats">
        {stats.map((s) => (
          <button
            className="overview-stat"
            key={s.label}
            onClick={() => navigate(s.view)}
          >
            <span>
              {s.label}
              <s.icon size={18} />
            </span>
            <strong>{s.value.toLocaleString("zh-CN")}</strong>
            <small>{s.hint}</small>
          </button>
        ))}
      </div>
      <div className="overview-columns">
        <section className="overview-panel">
          <header>
            <h2>
              <Server size={16} />
              邮箱一览
            </h2>
            <button
              className="secondary small"
              onClick={() => navigate("settings")}
            >
              管理邮箱
              <ArrowRight size={13} />
            </button>
          </header>
          {data.accounts.length ? (
            data.accounts.map((a) => {
              const status = a.paused
                ? "已暂停"
                : data.running[a.id]
                  ? "同步中"
                  : a.error
                    ? "连接异常"
                    : a.lastSync
                      ? "已同步"
                      : "等待同步";
              const count = data.mailboxes
                .filter((b) => b.accountId === a.id && b.role === "inbox")
                .reduce((n, b) => n + b.unread, 0);
              return (
                <button
                  key={a.id}
                  className="overview-account"
                  onClick={() =>
                    a.error ? onAccount(a) : navigate("inbox", "", a.id)
                  }
                >
                  <span className="overview-object-icon">
                    <Mail size={17} />
                  </span>
                  <span className="overview-account-copy">
                    <strong>{a.name}</strong>
                    <small>{a.address}</small>
                    <small>
                      {a.error ||
                        (a.lastSync
                          ? `最近同步 ${time(a.lastSync, true)}`
                          : "尚未完成首次同步")}
                    </small>
                  </span>
                  <span className="overview-account-state">
                    <span className={`badge ${a.error ? "danger" : ""}`}>
                      {status}
                    </span>
                    <small>{count} 封未读</small>
                  </span>
                  <ArrowRight size={13} />
                </button>
              );
            })
          ) : (
            <div className="overview-empty">
              <Mail size={26} />
              <h3>还没有邮箱</h3>
              <p>添加邮箱后，这里会显示连接与同步状态。</p>
              <button className="secondary" onClick={onAdd}>
                <Plus size={14} />
                添加邮箱
              </button>
            </div>
          )}
        </section>
        <section className="overview-panel">
          <header>
            <h2>
              <Clock size={16} />
              待处理事项
            </h2>
            <button
              className="secondary small"
              onClick={() => navigate("outbox")}
            >
              发件箱
              <ArrowRight size={13} />
            </button>
          </header>
          <div className="overview-tasks">
            {errors.length > 0 && (
              <button onClick={() => navigate("settings")}>
                <AlertCircle size={18} />
                <span>
                  <strong>{errors.length} 个邮箱连接异常</strong>
                  <small>检查连接配置与同步错误</small>
                </span>
                <ArrowRight size={14} />
              </button>
            )}
            <button onClick={() => navigate("drafts")}>
              <FileText size={18} />
              <span>
                <strong>
                  {data.drafts.length
                    ? `${data.drafts.length} 份草稿等待继续`
                    : "没有未完成的草稿"}
                </strong>
                <small>
                  {data.drafts.length
                    ? "继续编辑并发送你的邮件"
                    : "写下新邮件，编辑内容会自动保存"}
                </small>
              </span>
              <ArrowRight size={14} />
            </button>
            <button onClick={() => navigate("outbox")}>
              <Send size={18} />
              <span>
                <strong>
                  {pendingJobs.length
                    ? `${pendingJobs.length} 项发送任务待处理`
                    : "没有待处理的发送任务"}
                </strong>
                <small>
                  {pendingJobs.some((j) => j.state === "unknown")
                    ? "部分发送结果待确认，请先核对邮箱"
                    : "查看发送进度、失败与结果待确认的任务"}
                </small>
              </span>
              <ArrowRight size={14} />
            </button>
            {failedOps.length > 0 && (
              <button onClick={() => navigate("settings")}>
                <AlertCircle size={18} />
                <span>
                  <strong>{failedOps.length} 项邮件操作需要检查</strong>
                  <small>前往设置查看操作结果</small>
                </span>
                <ArrowRight size={14} />
              </button>
            )}
          </div>
          <div className="overview-actions">
            <button className="primary" onClick={onCompose}>
              <Plus size={15} />
              写邮件
            </button>
            <button
              className="secondary"
              disabled={
                !data.accounts.some((a) => !a.paused) ||
                syncing > 0 ||
                busy ||
                data.storage?.writable === false
              }
              onClick={() => void run(onSync)}
            >
              <RefreshCw size={14} className={syncing ? "spin" : ""} />
              同步邮箱
            </button>
          </div>
        </section>
      </div>
      <footer className="overview-note">
        <Settings2 size={14} />
        <span>邮件正文按需加载，已同步邮件可从收件箱及各文件夹查看。</span>
      </footer>
    </section>
  );
}

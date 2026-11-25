import { useEffect, useRef, useState, type FormEvent } from "react";
import { Clock, RefreshCw, History, ChevronDown } from "lucide-react";
import { api, time } from "./api";
import { useBackgroundRefresh } from "./useBackgroundRefresh";
import { Field, ErrorBox, Select } from "./components";
import type { Account, SyncStatus, SyncTask, SyncCounters } from "./types";
const names: Record<string, string> = {
  queued: "等待执行",
  running: "进行中",
  paused: "已暂停",
  succeeded: "已完成",
  partial_failed: "部分失败",
  failed: "失败",
  cancelled: "已取消",
  quick: "快速同步",
  history: "全历史同步",
  reconcile: "完整对齐",
  init: "等待处理",
  reset: "校验目录",
  scan: "导入与更新",
  delete: "核对删除",
  done: "已完成",
  removed: "目录已移除",
};
const stamp = (value?: string) =>
  value && !value.startsWith("0001-") ? time(value, true) : "尚无记录";
export function SyncPanel({
  account,
  status,
  onSaved,
  onBusyChange,
}: {
  account: Account;
  status?: SyncStatus;
  onSaved: () => Promise<void>;
  onBusyChange?: (busy: boolean) => void;
}) {
  const [scope, setScope] = useState(account.syncScope || "recent"),
    [days, setDays] = useState(account.syncDays),
    [enabled, setEnabled] = useState(account.reconcileEnabled !== false),
    [interval, setIntervalValue] = useState(
      account.reconcileIntervalMinutes || 15,
    ),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [tasks, setTasks] = useState<SyncTask[]>([]),
    [expanded, setExpanded] = useState(""),
    [logGroup, setLogGroup] = useState("full");
  useEffect(() => {
    onBusyChange?.(busy);
  }, [busy, onBusyChange]);
  const {
    refresh: refreshTasks,
    error: taskRefreshError,
    stop: stopTaskRefresh,
    clearError: clearTaskRefreshError,
  } = useBackgroundRefresh(async (signal) => {
    const results = await Promise.allSettled(
      ["full", "quick"].map((group) =>
        api<{ items: SyncTask[] }>(
          `/accounts/${account.id}/sync-tasks?group=${group}&limit=100`,
          "GET",
          undefined,
          signal,
        ),
      ),
    );
    const groups = results.map((result) => {
      if (result.status === "rejected") throw result.reason;
      return result.value;
    });
    if (groups.some((v) => !v || !Array.isArray(v.items)))
      throw new Error("同步任务响应无效");
    return [
      ...new Map(groups.flatMap((v) => v.items).map((t) => [t.id, t])).values(),
    ];
  }, setTasks);
  useEffect(() => {
    void refreshTasks();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void refreshTasks();
    }, 5000);
    return () => {
      stopTaskRefresh();
      clearTaskRefreshError();
      window.clearInterval(timer);
    };
  }, [account.id, refreshTasks, stopTaskRefresh, clearTaskRefreshError]);
  async function act(action: () => Promise<unknown>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await action();
      await refreshTasks();
      await onSaved();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    await act(() =>
      api(`/accounts/${account.id}/sync-settings`, "PUT", {
        scope,
        recentDays: days,
        reconcileEnabled: enabled,
        intervalMinutes: interval,
        expectedRevision: account.revision,
      }),
    );
  }
  const requestKeys = useRef<Record<string, string>>({});
  async function createTask(kind: string) {
    requestKeys.current[kind] ||= Array.from(
      crypto.getRandomValues(new Uint8Array(16)),
      (n) => n.toString(16).padStart(2, "0"),
    ).join("");
    const result = await api(`/accounts/${account.id}/sync-tasks`, "POST", {
      kind,
      requestKey: requestKeys.current[kind],
    });
    delete requestKeys.current[kind];
    return result;
  }
  const fullActive = tasks.some(
    (t) => t.kind !== "quick" && ["queued", "running"].includes(t.state),
  );
  const active = tasks.some((t) => ["queued", "running"].includes(t.state));
  const groupedTasks = tasks.filter((t) =>
    logGroup === "quick" ? t.kind === "quick" : t.kind !== "quick",
  );
  return (
    <section
      className="account-sync-panel"
      aria-label={`${account.name} 同步设置`}
    >
      <form onSubmit={save}>
        <div className="form-grid">
          <Field label="同步范围">
            <Select
              label="同步范围"
              value={scope}
              disabled={busy}
              onChange={(v) => setScope(v === "all" ? "all" : "recent")}
              options={[
                { value: "recent", label: "最近指定天数" },
                { value: "all", label: "全部历史" },
              ]}
            />
          </Field>
          <Field label="最近同步天数">
            <input
              type="number"
              min={1}
              max={3650}
              required
              disabled={scope === "all"}
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
            />
          </Field>
        </div>
        <label className="check">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          定时对齐服务器邮件
        </label>
        <Field label="对齐间隔（分钟）">
          <input
            type="number"
            min={1}
            max={1440}
            required
            value={interval}
            disabled={!enabled}
            onChange={(e) => setIntervalValue(Number(e.target.value))}
          />
        </Field>
        <button className="secondary small" disabled={busy}>
          保存同步设置
        </button>
      </form>
      <p className="muted sync-scope-note">
        同步全部历史邮件列表，正文及附件查看时加载，不是离线备份。对齐只更新本地索引，不会删除服务器邮件。
      </p>
      <dl className="sync-times">
        <div>
          <dt>最近完整对齐</dt>
          <dd>{stamp(status?.lastFullSuccessAt)}</dd>
        </div>
        <div>
          <dt>下次计划</dt>
          <dd>
            {account.paused
              ? "邮箱已暂停"
              : status?.errorKind === "authentication"
                ? "认证失败，等待处理"
                : !enabled
                  ? "定时对齐已关闭"
                  : stamp(status?.nextDueAt)}
          </dd>
        </div>
        {status?.error && (
          <div>
            <dt>任务提示</dt>
            <dd className="danger">
              {status.error}
              {status.errorKind !== "authentication" && status.retryAt
                ? ` · 重试 ${stamp(status.retryAt)}`
                : ""}
            </dd>
          </div>
        )}
      </dl>
      <div className="sync-buttons">
        <button
          className="secondary small"
          disabled={busy || account.paused || fullActive}
          onClick={() => void act(() => createTask("history"))}
        >
          <History size={14} />
          同步全部历史
        </button>
        <button
          className="secondary small"
          disabled={busy || account.paused || active}
          onClick={() => void act(() => createTask("reconcile"))}
        >
          <RefreshCw size={14} />
          立即对齐
        </button>
      </div>
      <ErrorBox error={error} />
      {taskRefreshError && (
        <div
          className="notice refresh-warning task-refresh-warning"
          role="status"
        >
          <span>
            任务状态暂时无法更新：{taskRefreshError}。已有进度仍可查看。
          </span>
          <button
            className="secondary small"
            onClick={() => void refreshTasks()}
          >
            重试读取
          </button>
        </div>
      )}

      <details className="sync-history">
        <summary>
          <History size={14} />
          <span>同步日志</span>
          <small>
            {tasks.length
              ? `${tasks.length} 条记录${active ? " · 有任务进行中" : ""}`
              : "暂无记录"}
          </small>
          <ChevronDown size={14} />
        </summary>
        <div className="sync-log-tabs" role="tablist" aria-label="同步日志分类">
          {[
            { id: "full", label: "完整对齐" },
            { id: "quick", label: "快速同步" },
          ].map((group) => (
            <button
              key={group.id}
              type="button"
              role="tab"
              id={`sync-tab-${group.id}`}
              aria-selected={logGroup === group.id}
              aria-controls="sync-log-panel"
              tabIndex={logGroup === group.id ? 0 : -1}
              onKeyDown={(e) => {
                if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key))
                  return;
                e.preventDefault();
                const next =
                  e.key === "Home"
                    ? "full"
                    : e.key === "End"
                      ? "quick"
                      : logGroup === "full"
                        ? "quick"
                        : "full";
                setLogGroup(next);
                setExpanded("");
                document.getElementById(`sync-tab-${next}`)?.focus();
              }}
              onClick={() => {
                setLogGroup(group.id);
                setExpanded("");
              }}
            >
              {group.label}{" "}
              <small>
                {
                  tasks.filter((t) =>
                    group.id === "quick"
                      ? t.kind === "quick"
                      : t.kind !== "quick",
                  ).length
                }
              </small>
            </button>
          ))}
        </div>
        <div
          id="sync-log-panel"
          className="sync-task-list"
          role="tabpanel"
          aria-labelledby={`sync-tab-${logGroup}`}
        >
          <p className="muted sync-log-note">
            每类保留最近 5 次已结束任务，进行中与暂停任务另行保留。
            {logGroup === "full"
              ? "全历史同步归入完整对齐。"
              : "快速同步只拉取新增邮件，不检查远端删除。"}
          </p>
          {!groupedTasks.length && (
            <p className="muted">
              暂无{logGroup === "full" ? "完整对齐" : "快速同步"}记录。
            </p>
          )}

          {groupedTasks.map((t) => {
            const done = (t.folders || []).filter((f) =>
                ["done", "removed"].includes(f.phase),
              ).length,
              processed = (t.folders || []).reduce(
                (n, f) => n + f.processed,
                0,
              ),
              current = (t.folders || []).find(
                (f) => !["done", "failed", "removed"].includes(f.phase),
              );
            return (
              <article key={t.id}>
                <div className="sync-task-heading">
                  <button
                    className="sync-task-toggle"
                    onClick={() => setExpanded(expanded === t.id ? "" : t.id)}
                    aria-expanded={expanded === t.id}
                  >
                    <Clock size={13} />
                    <span>
                      {names[t.kind]} · {names[t.state]}
                    </span>
                    <ChevronDown size={13} />
                  </button>
                  {["queued", "running"].includes(t.state) ? (
                    <button
                      className="secondary small"
                      disabled={busy}
                      onClick={() =>
                        void act(() =>
                          api(`/sync-tasks/${t.id}/cancel`, "POST"),
                        )
                      }
                    >
                      取消
                    </button>
                  ) : (
                    t.state !== "succeeded" && (
                      <button
                        className="secondary small"
                        disabled={busy || account.paused || active}
                        onClick={() =>
                          void act(() =>
                            api(`/sync-tasks/${t.id}/resume`, "POST"),
                          )
                        }
                      >
                        继续 / 重试
                      </button>
                    )
                  )}
                </div>
                <p className="muted">
                  已处理 {processed} 封 · 文件夹 {done}/
                  {(t.folders || []).length}
                  {current ? ` · ${current.name}` : ""}
                </p>
                {t.logVersion ? (
                  <p className="sync-task-stats">{counterText(t.stats)}</p>
                ) : (
                  <p className="muted">旧版任务未记录新增、删除明细。</p>
                )}
                <p className="muted">
                  {stamp(t.createdAt)} ·{" "}
                  {t.trigger === "timer"
                    ? "定时触发"
                    : t.trigger === "manual"
                      ? "手动触发"
                      : "触发来源未记录"}
                </p>
                {t.error && <p className="danger">{t.error}</p>}
                {expanded === t.id && (
                  <div className="sync-task-details">
                    <dl className="sync-log-times">
                      <div>
                        <dt>开始</dt>
                        <dd>{stamp(t.startedAt)}</dd>
                      </div>
                      <div>
                        <dt>结束</dt>
                        <dd>{stamp(t.finishedAt)}</dd>
                      </div>
                      <div>
                        <dt>补录范围</dt>
                        <dd>
                          {t.scope === "all"
                            ? "全部历史"
                            : t.scope === "recent"
                              ? `最近 ${t.recentDays || account.syncDays} 天`
                              : "旧版任务未记录"}
                        </dd>
                      </div>
                    </dl>
                    <p className="muted">
                      对比为已提交的核对次数，包含删除二次确认；更新指邮件标记或元数据变化，未补录指不在补录范围内的邮件。重试统计累计，删除仅影响本地索引。
                    </p>
                    <ul className="sync-folder-logs">
                      {(t.folders || []).map((f) => (
                        <li key={f.mailboxId}>
                          <strong>{f.name}</strong> ·{" "}
                          {names[f.phase] || f.phase} · 已处理 {f.processed} 封
                          {t.logVersion && (
                            <>
                              <p>
                                {f.snapshotAt || f.phase === "removed"
                                  ? `扫描前本地 ${f.localBefore ?? 0} 封 · 服务器 ${f.remoteTotal ?? 0} 封（目录总数）`
                                  : "目录数量尚未记录"}
                              </p>
                              <p>{counterText(f.stats)}</p>
                            </>
                          )}
                          {f.error && (
                            <span className="danger"> · {f.error}</span>
                          )}
                        </li>
                      ))}
                    </ul>
                    {t.logsDropped ? (
                      <p className="muted">
                        较早的 {t.logsDropped}{" "}
                        条过程明细已省略，累计统计完整保留。
                      </p>
                    ) : null}
                    <ol className="sync-event-logs" aria-label="任务详细日志">
                      {(t.logs || []).map((event, index) => (
                        <li key={`${event.at}-${index}`}>
                          <time>{stamp(event.at)}</time>
                          <span>
                            {event.folder ? `${event.folder} · ` : ""}
                            {event.message}
                          </span>
                          {event.firstUid ? (
                            <small>
                              UID {event.firstUid}–
                              {event.lastUid || event.firstUid}
                            </small>
                          ) : null}
                          {event.stats?.compared > 0 && (
                            <small>{counterText(event.stats)}</small>
                          )}
                        </li>
                      ))}
                    </ol>
                  </div>
                )}
              </article>
            );
          })}
        </div>
      </details>
    </section>
  );
}

function counterText(stats?: SyncCounters) {
  return `对比 ${stats?.compared || 0} · 新增 ${stats?.added || 0} · 更新 ${stats?.updated || 0} · 未变 ${stats?.unchanged || 0} · 删除 ${stats?.deleted || 0} · 未补录 ${stats?.skipped || 0}`;
}

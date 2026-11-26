import { useState } from "react";
import { Clock } from "lucide-react";
import { api } from "./api";
import type { Operation } from "./types";

export function OperationPanel({
  operations,
  onSaved,
  onError,
}: {
  operations: Operation[];
  onSaved: () => Promise<void>;
  onError: (message: string) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const failed = operations.filter((o) => o.state === "failed");
  async function act(path: string, method: "POST" | "DELETE", message: string) {
    if (busy) return;
    setBusy(true);
    setNotice("");
    try {
      await api(path, method);
      setNotice(message);
      await onSaved();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="preference-section" aria-label="未完成的邮件操作">
      <h2>
        <Clock size={17} />
        未完成的邮件操作
      </h2>
      <p className="muted">
        移动、删除先在本地移出列表，后台再提交；失败时恢复显示。移动结果不确定时可同步核对来源和目标目录，不会重新执行移动。
      </p>
      <p className="muted">
        清除仅移除失败记录，不删除邮件、不撤销远端操作。等待和执行中的任务不能清除。
      </p>
      {failed.length > 0 && (
        <button
          className="secondary small"
          disabled={busy}
          onClick={() => void act("/operations", "DELETE", "已清除失败记录")}
        >
          清除全部失败记录（{failed.length}）
        </button>
      )}
      {notice && (
        <p className="muted" role="status">
          {notice}
        </p>
      )}
      {operations.length ? (
        operations.map((o) => (
          <div className="operation-row" key={o.id}>
            <div>
              <strong>
                {o.kind === "seen"
                  ? "已读状态"
                  : o.kind === "starred"
                    ? "星标状态"
                    : o.kind === "trash"
                      ? "删除邮件"
                      : "移动邮件"}{" "}
                ·{" "}
                {o.state === "queued"
                  ? "等待同步"
                  : o.state === "running"
                    ? "执行中"
                    : "失败"}
              </strong>
              {o.error && <p className="danger">{o.error}</p>}
            </div>
            {o.state === "failed" && (
              <div className="operation-actions">
                {["seen", "starred"].includes(o.kind) ? (
                  <button
                    className="secondary small"
                    disabled={busy}
                    onClick={() =>
                      void act(
                        `/operations/${o.id}/retry`,
                        "POST",
                        "已加入重试队列",
                      )
                    }
                  >
                    重试
                  </button>
                ) : (
                  ["move", "trash"].includes(o.kind) && (
                    <button
                      className="secondary small"
                      disabled={busy}
                      onClick={() =>
                        void act(
                          `/operations/${o.id}/reconcile`,
                          "POST",
                          "已提交同步核对，请等待完成并确认邮件位置后清除记录",
                        )
                      }
                    >
                      同步核对
                    </button>
                  )
                )}
                <button
                  className="secondary small"
                  disabled={busy}
                  onClick={() =>
                    void act(`/operations/${o.id}`, "DELETE", "已清除失败记录")
                  }
                >
                  清除记录
                </button>
              </div>
            )}
          </div>
        ))
      ) : (
        <p className="muted">没有未完成的操作。</p>
      )}
    </section>
  );
}

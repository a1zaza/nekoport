import { useState, type FormEvent } from "react";
import { RefreshCw, Save, Server, Send } from "lucide-react";
import { api } from "./api";
import { Modal, Field, Select, ErrorBox } from "./components";
import type { Account, Endpoint } from "./types";
export function AccountEditor({
  account,
  onClose,
  onSaved,
}: {
  account?: Account;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [value, setValue] = useState<Account>(
    account || {
      id: "",
      name: "",
      address: "",
      imap: { host: "", port: 993, tls: "tls", username: "" },
      smtp: { host: "", port: 465, tls: "tls", username: "" },
      credentialRef: "",
      paused: false,
      syncDays: 30,
      autoArchive: false,
      revision: 0,
    },
  );
  const [password, setPassword] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [result, setResult] = useState<Record<string, string> | null>(null);
  function endpoint(
    kind: "imap" | "smtp",
    k: keyof Endpoint,
    v: string | number,
  ) {
    setValue((x) => ({ ...x, [kind]: { ...x[kind], [k]: v } }));
  }
  function request() {
    return {
      ...value,
      useSameCredentials: true,
      smtp: { ...value.smtp, username: value.imap.username },
      imapPassword: password,
      smtpPassword: password,
    };
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api(
        value.id ? `/accounts/${value.id}` : "/accounts",
        value.id ? "PUT" : "POST",
        request(),
      );
      onSaved();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function check() {
    setBusy(true);
    setError("");
    setResult(null);
    try {
      setResult(await api("/connection-checks", "POST", request()));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={account ? "编辑邮箱" : "添加邮箱"}
      description="手动填写邮件服务器信息，接收与发送分别建立安全连接。"
      onClose={() => {
        if (!busy) onClose();
      }}
      wide
    >
      <form onSubmit={save}>
        <fieldset disabled={busy}>
          <div className="form-grid">
            <Field label="显示名称">
              <input
                required
                maxLength={80}
                value={value.name}
                onChange={(e) => setValue({ ...value, name: e.target.value })}
                placeholder="例如：工作邮箱"
              />
            </Field>
            <Field label="邮箱地址">
              <input
                required
                type="email"
                value={value.address}
                onChange={(e) =>
                  setValue({ ...value, address: e.target.value })
                }
                placeholder="你的完整邮箱地址"
              />
            </Field>
          </div>
          <section className="shared-login">
            <h3>登录信息</h3>
            <p className="muted">IMAP 接收与 SMTP 发送共用以下账号和密码。</p>
            <div className="form-grid">
              <Field label="登录用户名">
                <input
                  required
                  autoComplete="off"
                  value={value.imap.username}
                  onChange={(e) => {
                    const username = e.target.value;
                    setValue((v) => ({
                      ...v,
                      imap: { ...v.imap, username },
                      smtp: { ...v.smtp, username },
                    }));
                  }}
                />
              </Field>
              <Field
                label="密码 / 应用密码"
                hint={
                  account
                    ? "留空使用已保存的接收密码"
                    : "加密保存，用于接收与发送"
                }
              >
                <input
                  type="password"
                  aria-label="密码 / 应用密码"
                  required={!account}
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
            </div>
          </section>
          <div className="server-columns">
            {(["imap", "smtp"] as const).map((kind) => (
              <section key={kind} className="server-section">
                <h3>
                  {kind === "imap" ? <Server size={16} /> : <Send size={16} />}{" "}
                  {kind === "imap" ? "接收服务器 · IMAP" : "发送服务器 · SMTP"}
                </h3>
                <Field label="服务器地址">
                  <input
                    required
                    value={value[kind].host}
                    onChange={(e) => endpoint(kind, "host", e.target.value)}
                    placeholder="由邮箱提供方告知的主机名"
                  />
                </Field>
                <div className="form-grid">
                  <Field label="端口">
                    <input
                      required
                      inputMode="numeric"
                      pattern="[0-9]+"
                      value={value[kind].port || ""}
                      onChange={(e) =>
                        endpoint(
                          kind,
                          "port",
                          /^\d*$/.test(e.target.value)
                            ? Number(e.target.value)
                            : 0,
                        )
                      }
                    />
                  </Field>
                  <Field label="加密方式">
                    <Select
                      label={`${kind} 加密方式`}
                      value={value[kind].tls}
                      onChange={(v) => endpoint(kind, "tls", v)}
                      options={[
                        { value: "tls", label: "TLS" },
                        { value: "starttls", label: "STARTTLS" },
                      ]}
                    />
                  </Field>
                </div>
              </section>
            ))}
          </div>
          <div className="form-grid">
            <Field label="首次同步范围（天）">
              <input
                required
                inputMode="numeric"
                pattern="[0-9]+"
                value={value.syncDays || ""}
                onChange={(e) =>
                  setValue({ ...value, syncDays: Number(e.target.value) })
                }
              />
            </Field>
            <Field label="IMAP 连接上限（4–50）">
              <input
                type="number"
                min={4}
                max={50}
                required
                value={value.imapMaxConnections || 10}
                onChange={(e) =>
                  setValue({
                    ...value,
                    imapMaxConnections: Number(e.target.value),
                  })
                }
              />
              <small className="muted">
                默认 10，预留 3 个连接用于邮件操作与正文读取。
              </small>
            </Field>
            <div className="account-options">
              <label className="check">
                <input
                  type="checkbox"
                  checked={value.autoArchive}
                  onChange={(e) =>
                    setValue({ ...value, autoArchive: e.target.checked })
                  }
                />
                服务器会自动保存已发送邮件
              </label>
              <label className="check">
                <input
                  type="checkbox"
                  checked={value.paused}
                  onChange={(e) =>
                    setValue({ ...value, paused: e.target.checked })
                  }
                />
                暂停此邮箱的后台同步
              </label>
            </div>
          </div>
          {result && (
            <div className="connection-result">
              {Object.entries(result).map(([k, v]) => (
                <p key={k} className={v === "连接成功" ? "success" : "danger"}>
                  {k.toUpperCase()}：{v}
                </p>
              ))}
            </div>
          )}
          <ErrorBox error={error} />
        </fieldset>
        <footer className="modal-footer">
          <button
            type="button"
            className="secondary"
            disabled={busy}
            onClick={check}
          >
            <RefreshCw size={15} className={busy ? "spin" : ""} />
            检查连接
          </button>
          <span className="spacer" />
          <button
            type="button"
            className="secondary"
            disabled={busy}
            onClick={onClose}
          >
            取消
          </button>
          <button className="primary" disabled={busy}>
            <Save size={15} />
            {busy ? "处理中…" : "保存邮箱"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}

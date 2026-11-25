import { useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import { api } from "./api";
import { Field, ErrorBox } from "./components";

export function AccountSecurity({
  account,
  blocked,
  onSuccess,
}: {
  account: { username: string; revision: number };
  blocked: boolean;
  onSuccess: () => void;
}) {
  const [username, setUsername] = useState(account.username),
    [changePassword, setChangePassword] = useState(false),
    [current, setCurrent] = useState(""),
    [password, setPassword] = useState(""),
    [confirmation, setConfirmation] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const changed = username.trim() !== account.username || changePassword;
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || blocked || !changed) return;
    setError("");
    if (
      !username.trim() ||
      new TextEncoder().encode(username.trim()).length > 64 ||
      /[\u0000-\u001f\u007f-\u009f]/.test(username)
    ) {
      setError("用户名应为 1–64 字节，不能包含控制字符");
      return;
    }
    if (changePassword) {
      if (
        [...password].length < 8 ||
        new TextEncoder().encode(password).length > 72
      ) {
        setError("新密码至少 8 个字符，最多 72 字节");
        return;
      }
      if (password !== confirmation) {
        setError("两次输入的新密码不一致");
        return;
      }
      if (password === current) {
        setError("新密码不能与当前密码相同");
        return;
      }
    }
    setBusy(true);
    try {
      await api("/auth/account", "PUT", {
        username: username.trim(),
        currentPassword: current,
        ...(changePassword ? { newPassword: password } : {}),
        expectedRevision: account.revision,
      });
      setCurrent("");
      setPassword("");
      setConfirmation("");
      onSuccess();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="preference-section account-security">
      <h2>
        <KeyRound size={17} />
        账户安全
      </h2>
      <p className="muted">
        修改本平台登录账户，不影响邮箱连接密码。保存成功后，所有会话都需重新登录。
      </p>
      <form onSubmit={submit}>
        <fieldset disabled={busy || blocked}>
          <Field label="登录用户名">
            <input
              value={username}
              autoComplete="username"
              required
              onChange={(e) => setUsername(e.target.value)}
            />
          </Field>
          <label className="toggle-row">
            <span>
              修改密码<small>仅修改用户名时无需开启。</small>
            </span>
            <input
              type="checkbox"
              role="switch"
              aria-label="修改密码"
              checked={changePassword}
              onChange={(e) => {
                setChangePassword(e.target.checked);
                setPassword("");
                setConfirmation("");
                setError("");
              }}
            />
          </label>
          {changePassword && (
            <div className="form-grid">
              <Field label="新密码" hint="至少 8 个字符，最多 72 字节">
                <input
                  type="password"
                  autoComplete="new-password"
                  aria-label="新密码"
                  required
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              <Field label="确认新密码">
                <input
                  type="password"
                  autoComplete="new-password"
                  required
                  value={confirmation}
                  onChange={(e) => setConfirmation(e.target.value)}
                />
              </Field>
            </div>
          )}
          <Field label="当前密码" hint="修改用户名或密码均需验证当前密码">
            <input
              type="password"
              autoComplete="current-password"
              aria-label="当前密码"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </Field>
        </fieldset>
        <ErrorBox error={error} />
        {blocked && (
          <p className="danger">请先保存并关闭写信窗口，再修改登录账户。</p>
        )}
        <button
          className="secondary"
          disabled={busy || blocked || !changed || !current || !username.trim()}
        >
          {busy ? "保存中…" : "保存并重新登录"}
        </button>
      </form>
    </section>
  );
}

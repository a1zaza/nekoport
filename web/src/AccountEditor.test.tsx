import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AccountEditor } from "./AccountEditor";
import { api } from "./api";
import type { Account } from "./types";
vi.mock("./api", () => ({ api: vi.fn() }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
it("新建邮箱只填写一组登录信息，并同时提交给接收和发送", async () => {
  vi.mocked(api).mockResolvedValue({});
  const closed = vi.fn();
  render(<AccountEditor onClose={closed} onSaved={() => {}} />);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("显示名称"), "测试邮箱");
  await user.type(screen.getByLabelText("邮箱地址"), "user@example.test");
  await user.type(
    screen.getByLabelText("登录用户名", { exact: true }),
    "shared-user",
  );
  await user.type(
    screen.getByLabelText("密码 / 应用密码", { exact: true }),
    "shared-password",
  );
  const hosts = screen.getAllByLabelText("服务器地址");
  await user.type(hosts[0], "imap.example.test");
  await user.type(hosts[1], "smtp.example.test");
  await user.click(screen.getByRole("button", { name: "保存邮箱" }));
  await waitFor(() => expect(closed).toHaveBeenCalledOnce());
  expect(api).toHaveBeenCalledWith(
    "/accounts",
    "POST",
    expect.objectContaining({
      useSameCredentials: true,
      imapPassword: "shared-password",
      smtpPassword: "shared-password",
      imap: expect.objectContaining({ username: "shared-user" }),
      smtp: expect.objectContaining({ username: "shared-user" }),
    }),
  );
});
it("编辑旧邮箱时仅展示接收用户名，留空密码的检查与保存都启用共享凭据", async () => {
  const account: Account = {
    id: "a",
    name: "邮箱",
    address: "user@example.test",
    imap: {
      host: "imap.example.test",
      port: 993,
      tls: "tls",
      username: "imap-user",
    },
    smtp: {
      host: "smtp.example.test",
      port: 465,
      tls: "tls",
      username: "legacy-smtp-user",
    },
    credentialRef: "",
    paused: true,
    syncDays: 30,
    autoArchive: false,
    revision: 1,
  };
  vi.mocked(api).mockResolvedValue({ imap: "连接成功", smtp: "连接成功" });
  const closed = vi.fn();
  render(
    <AccountEditor account={account} onClose={closed} onSaved={() => {}} />,
  );
  expect(screen.getAllByLabelText("登录用户名", { exact: true })).toHaveLength(
    1,
  );
  expect(
    (
      screen.getByLabelText("密码 / 应用密码", {
        exact: true,
      }) as HTMLInputElement
    ).value,
  ).toBe("");
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "检查连接" }));
  await waitFor(() =>
    expect(api).toHaveBeenCalledWith(
      "/connection-checks",
      "POST",
      expect.objectContaining({
        useSameCredentials: true,
        imapPassword: "",
        smtpPassword: "",
        smtp: expect.objectContaining({ username: "imap-user" }),
      }),
    ),
  );
  await user.click(screen.getByRole("button", { name: "保存邮箱" }));
  await waitFor(() => expect(closed).toHaveBeenCalledOnce());
  expect(api).toHaveBeenLastCalledWith(
    "/accounts/a",
    "PUT",
    expect.objectContaining({
      useSameCredentials: true,
      imapPassword: "",
      smtpPassword: "",
      smtp: expect.objectContaining({ username: "imap-user" }),
    }),
  );
});

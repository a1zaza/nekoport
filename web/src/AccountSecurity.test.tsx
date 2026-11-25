import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AccountSecurity } from "./AccountSecurity";
import { api } from "./api";
vi.mock("./api", () => ({ api: vi.fn() }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
it("先校验密码确认与字节长度，失败保留输入，关闭开关只修改用户名", async () => {
  const success = vi.fn();
  render(
    <AccountSecurity
      account={{ username: "admin", revision: 3 }}
      blocked={false}
      onSuccess={success}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("switch", { name: "修改密码" }));
  await user.type(
    screen.getByLabelText("新密码", { exact: true }),
    "new-password",
  );
  await user.type(
    screen.getByLabelText("确认新密码", { exact: true }),
    "different-password",
  );
  await user.type(
    screen.getByLabelText("当前密码", { exact: true }),
    "current-password",
  );
  await user.click(screen.getByRole("button", { name: "保存并重新登录" }));
  expect(screen.getByRole("alert").textContent).toContain("不一致");
  expect(api).not.toHaveBeenCalled();
  await user.clear(screen.getByLabelText("确认新密码", { exact: true }));
  await user.type(
    screen.getByLabelText("确认新密码", { exact: true }),
    "new-password",
  );
  vi.mocked(api).mockRejectedValueOnce(new Error("当前密码不正确"));
  await user.click(screen.getByRole("button", { name: "保存并重新登录" }));
  await waitFor(() =>
    expect(screen.getByRole("alert").textContent).toContain("当前密码不正确"),
  );
  expect(
    (screen.getByLabelText("新密码", { exact: true }) as HTMLInputElement)
      .value,
  ).toBe("new-password");
  await user.click(screen.getByRole("switch", { name: "修改密码" }));
  await user.clear(screen.getByLabelText("登录用户名"));
  await user.type(screen.getByLabelText("登录用户名"), "new-admin");
  vi.mocked(api).mockResolvedValueOnce({ reauthenticate: true });
  await user.click(screen.getByRole("button", { name: "保存并重新登录" }));
  await waitFor(() => expect(success).toHaveBeenCalledOnce());
  expect(vi.mocked(api).mock.calls.at(-1)?.[2]).toEqual({
    username: "new-admin",
    currentPassword: "current-password",
    expectedRevision: 3,
  });
});
it("有写信窗口时阻止修改账户", () => {
  render(
    <AccountSecurity
      account={{ username: "admin", revision: 1 }}
      blocked={true}
      onSuccess={() => {}}
    />,
  );
  expect(
    (
      screen.getByRole("button", {
        name: "保存并重新登录",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(
    screen.getByText("请先保存并关闭写信窗口，再修改登录账户。"),
  ).toBeTruthy();
});

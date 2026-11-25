import { afterEach, describe, it, expect, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Compose } from "./Compose";
import { api } from "./api";
import type { Account, Draft } from "./types";
vi.mock("./api", async () => ({
  ...(await vi.importActual("./api")),
  api: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.unstubAllGlobals();
});
const account: Account = {
  id: "a",
  name: "测试邮箱",
  address: "a@example.test",
  imap: { host: "imap.example.test", port: 993, tls: "tls", username: "a" },
  smtp: { host: "smtp.example.test", port: 465, tls: "tls", username: "a" },
  credentialRef: "",
  paused: true,
  syncDays: 30,
  autoArchive: false,
  revision: 1,
};
const draft: Draft = {
  id: "draft",
  accountId: "a",
  to: "to@example.test",
  cc: "",
  bcc: "",
  subject: "主题",
  bodyId: "body",
  attachments: [],
  revision: 1,
  updatedAt: "",
};
describe("草稿与发送的并发边界", () => {
  it("等待旧保存完成后，先保存最新正文再发送", async () => {
    let release!: (d: Draft) => void;
    const requests: { path: string; value: any }[] = [];
    vi.mocked(api).mockImplementation(async (path, method, value) => {
      requests.push({ path, value });
      if (requests.length === 1)
        return new Promise((resolve) => {
          release = resolve as typeof release;
        });
      if (path.endsWith("/send")) return { id: "job", state: "queued" };
      return { ...draft, revision: 3 };
    });
    const closed = vi.fn();
    render(
      <Compose
        initial={{ draft, body: "" }}
        accounts={[account]}
        onClose={closed}
        onSaved={() => {}}
      />,
    );
    const user = userEvent.setup();
    const text = screen.getByRole("textbox", { name: "纯文本正文" });
    await user.type(text, "旧正文");
    await user.click(screen.getByRole("button", { name: "保存草稿" }));
    await waitFor(() => expect(requests).toHaveLength(1));
    await user.clear(text);
    await user.type(text, "等待保存期间编辑的新正文");
    await user.click(screen.getByRole("button", { name: /^发送$/ }));
    release({ ...draft, revision: 2 });
    await waitFor(() => expect(closed).toHaveBeenCalledTimes(1));
    expect(requests).toHaveLength(3);
    expect(requests[1].value.body).toBe("等待保存期间编辑的新正文");
    expect(requests[1].value.revision).toBe(2);
    expect(requests[2].path).toBe("/drafts/draft/send");
    expect(requests[2].value.revision).toBe(3);
  });
  it("保存失败时保留编辑并阻止发送", async () => {
    vi.mocked(api).mockRejectedValue(new Error("保存结果待确认"));
    const closed = vi.fn();
    render(
      <Compose
        initial={{ draft, body: "不能丢失的正文" }}
        accounts={[account]}
        onClose={closed}
        onSaved={() => {}}
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: /^发送$/ }));
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain("保存结果待确认"),
    );
    expect(
      (
        screen.getByRole("textbox", {
          name: "纯文本正文",
        }) as HTMLTextAreaElement
      ).value,
    ).toBe("不能丢失的正文");
    expect(closed).not.toHaveBeenCalled();
    expect(
      vi.mocked(api).mock.calls.every(([path]) => !path.endsWith("/send")),
    ).toBe(true);
  });
});

it("局域网 HTTP 缺少 randomUUID 时仍可写信，重试发送复用同一幂等键", async () => {
  const getRandomValues = globalThis.crypto.getRandomValues.bind(
    globalThis.crypto,
  );
  vi.stubGlobal("crypto", { getRandomValues });
  const keys: string[] = [];
  vi.mocked(api).mockImplementation(async (path, _method, value) => {
    if (path.endsWith("/send")) {
      keys.push((value as { key: string }).key);
      if (keys.length === 1) throw new Error("网络中断，请重试");
      return { id: "job", state: "queued" };
    }
    return { ...draft, revision: 2 };
  });
  const closed = vi.fn();
  render(
    <Compose
      initial={{ draft, body: "局域网写信" }}
      accounts={[account]}
      onClose={closed}
      onSaved={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /^发送$/ }));
  await waitFor(() =>
    expect(screen.getByRole("alert").textContent).toContain("网络中断"),
  );
  expect(closed).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: /^发送$/ }));
  await waitFor(() => expect(closed).toHaveBeenCalledOnce());
  expect(keys).toHaveLength(2);
  expect(keys[0]).toMatch(/^[a-f0-9]{32}$/);
  expect(keys[1]).toBe(keys[0]);
});

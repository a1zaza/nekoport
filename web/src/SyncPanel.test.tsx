import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SyncPanel } from "./SyncPanel";
import { api } from "./api";
import type { Account, SyncTask } from "./types";
vi.mock("./api", async (load) => ({
  ...(await load<typeof import("./api")>()),
  api: vi.fn(),
}));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
it("按完整对齐和快速同步分类显示日志、对比统计及旧日志提示", async () => {
  const stats = { compared: 8, added: 3, updated: 1, unchanged: 2, deleted: 2 };
  const task: SyncTask = {
    id: "full",
    accountId: "a",
    kind: "reconcile",
    state: "succeeded",
    logVersion: 1,
    stats,
    trigger: "timer",
    scope: "all",
    createdAt: "2026-11-25T03:00:00Z",
    folders: [
      {
        mailboxId: "inbox",
        name: "收件箱",
        phase: "done",
        processed: 6,
        snapshotAt: "2026-11-25T03:00:00Z",
        localBefore: 7,
        remoteTotal: 8,
        stats,
      },
    ],
    logs: [
      {
        at: "2026-11-25T03:00:00Z",
        action: "delete-confirm",
        folder: "收件箱",
        message: "二次精确查询确认邮件是否仍存在",
        firstUid: 5,
        lastUid: 7,
        stats,
      },
    ],
  };
  vi.mocked(api).mockImplementation(async (url) => ({
    items: String(url).includes("group=quick")
      ? [
          {
            ...task,
            id: "quick",
            kind: "quick",
            logVersion: undefined,
            logs: [],
          },
        ]
      : [task],
  }));
  render(
    <SyncPanel
      account={{ id: "a", name: "工作邮箱", syncDays: 30 } as Account}
      onSaved={async () => {}}
    />,
  );
  const user = userEvent.setup();
  await waitFor(() => expect(api).toHaveBeenCalledTimes(2));
  await user.click(screen.getByText("同步日志", { exact: true }));
  const full = screen.getByRole("tab", { name: /完整对齐/ });
  expect(full.getAttribute("aria-selected")).toBe("true");
  expect(screen.queryByRole("button", { name: /快速同步 ·/ })).toBeNull();
  await user.click(
    await screen.findByRole("button", { name: /完整对齐 · 已完成/ }),
  );
  const panel = screen.getByRole("tabpanel");
  expect(
    within(panel).getByText("扫描前本地 7 封 · 服务器 8 封（目录总数）"),
  ).toBeTruthy();
  expect(
    within(panel).getAllByText(/对比 8 · 新增 3 · 更新 1 · 未变 2 · 删除 2/)
      .length,
  ).toBeGreaterThan(0);
  expect(within(panel).getByText(/二次精确查询/)).toBeTruthy();
  expect(within(panel).getByText("UID 5–7")).toBeTruthy();
  await user.click(screen.getByRole("tab", { name: /快速同步/ }));
  expect(screen.queryByRole("button", { name: /完整对齐 ·/ })).toBeNull();
  expect(screen.getByText("旧版任务未记录新增、删除明细。")).toBeTruthy();
  expect(screen.getByText(/快速同步由 IDLE 新邮件事件或手动触发/)).toBeTruthy();
  await user.keyboard("{ArrowLeft}");
  expect(full.getAttribute("aria-selected")).toBe("true");
});

it("显示 IDLE 的部分连接、重连提示及暂停状态", async () => {
  vi.mocked(api).mockResolvedValue({ items: [] });
  const account = { id: "a", name: "工作邮箱", syncDays: 30 } as Account;
  const idle = {
    state: "reconnecting",
    connected: 2,
    total: 3,
    error: "IDLE 连接已断开",
    retryAt: "2026-11-25T03:00:00Z",
  };
  const { rerender } = render(
    <SyncPanel account={account} idle={idle} onSaved={async () => {}} />,
  );
  expect(screen.getByText(/断线重连中 · 2\/3 个文件夹/)).toBeTruthy();
  expect(screen.getByText(/IDLE 连接已断开 · 重试/)).toBeTruthy();
  rerender(
    <SyncPanel
      account={account}
      idle={{ ...idle, state: "unsupported", error: "服务器不支持 IMAP IDLE" }}
      onSaved={async () => {}}
    />,
  );
  expect(screen.getByText(/服务器不支持 IDLE · 2\/3 个文件夹/)).toBeTruthy();
  expect(screen.queryByText(/IDLE 连接已断开/)).toBeNull();
  rerender(
    <SyncPanel
      account={{ ...account, paused: true }}
      idle={idle}
      onSaved={async () => {}}
    />,
  );
  expect(screen.getAllByText("邮箱已暂停").length).toBeGreaterThan(0);
  expect(screen.queryByText(/IDLE 连接已断开/)).toBeNull();
  await waitFor(() => expect(api).toHaveBeenCalledTimes(2));
});

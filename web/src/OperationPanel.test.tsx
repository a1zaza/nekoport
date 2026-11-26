import { afterEach, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { OperationPanel } from "./OperationPanel";
import { api } from "./api";
import type { Operation } from "./types";
vi.mock("./api", () => ({ api: vi.fn() }));
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});
const failed = (kind: string, id = kind): Operation => ({
  id,
  accountId: "a",
  messageId: "m",
  kind,
  state: "failed",
  error: `${kind} 失败`,
});

it("失败标记可重试、单条清除，移动只提供核对及清除", async () => {
  vi.mocked(api).mockResolvedValue({ ok: true });
  const saved = vi.fn().mockResolvedValue(undefined);
  render(
    <OperationPanel
      operations={[failed("seen"), failed("move")]}
      onSaved={saved}
      onError={vi.fn()}
    />,
  );
  const user = userEvent.setup();
  const move = screen
    .getByText("move 失败")
    .closest(".operation-row") as HTMLElement;
  expect(within(move).queryByRole("button", { name: "重试" })).toBeNull();
  await user.click(within(move).getByRole("button", { name: "同步核对" }));
  await waitFor(() => expect(saved).toHaveBeenCalledTimes(1));
  expect(api).toHaveBeenLastCalledWith("/operations/move/reconcile", "POST");
  await user.click(screen.getByRole("button", { name: "重试" }));
  await waitFor(() => expect(saved).toHaveBeenCalledTimes(2));
  expect(api).toHaveBeenLastCalledWith("/operations/seen/retry", "POST");
  await user.click(within(move).getByRole("button", { name: "清除记录" }));
  await waitFor(() => expect(saved).toHaveBeenCalledTimes(3));
  expect(api).toHaveBeenLastCalledWith("/operations/move", "DELETE");
});

it("清除全部仅针对失败，活动任务没有清除入口且请求期间禁用操作", async () => {
  let release!: (value: unknown) => void;
  vi.mocked(api).mockReturnValue(
    new Promise((resolve) => {
      release = resolve;
    }),
  );
  const saved = vi.fn().mockResolvedValue(undefined);
  render(
    <OperationPanel
      operations={[
        failed("trash"),
        { ...failed("move", "pending"), state: "queued" },
      ]}
      onSaved={saved}
      onError={vi.fn()}
    />,
  );
  const user = userEvent.setup();
  expect(screen.getAllByRole("button", { name: "清除记录" }).length).toBe(1);
  await user.click(
    screen.getByRole("button", { name: "清除全部失败记录（1）" }),
  );
  expect(api).toHaveBeenCalledWith("/operations", "DELETE");
  expect(
    (screen.getByRole("button", { name: "清除记录" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  expect(saved).not.toHaveBeenCalled();
  release({ ok: true });
  await waitFor(() => expect(saved).toHaveBeenCalledTimes(1));
});

it("清除失败保留记录并显示错误，不声称已清除", async () => {
  vi.mocked(api).mockRejectedValue(new Error("写入失败"));
  const error = vi.fn(),
    saved = vi.fn();
  render(
    <OperationPanel
      operations={[failed("trash")]}
      onSaved={saved}
      onError={error}
    />,
  );
  await userEvent
    .setup()
    .click(screen.getByRole("button", { name: "清除记录" }));
  await waitFor(() => expect(error).toHaveBeenCalledWith("写入失败"));
  expect(saved).not.toHaveBeenCalled();
  expect(screen.getByText("trash 失败")).toBeTruthy();
  expect(screen.queryByText("已清除失败记录")).toBeNull();
});

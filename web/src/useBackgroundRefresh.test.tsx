import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { useBackgroundRefresh } from "./useBackgroundRefresh";
import { NetworkError } from "./api";
afterEach(cleanup);
it("后台读取失败保留旧数据，之后成功只清理自己的刷新错误", async () => {
  const load = vi
    .fn()
    .mockResolvedValueOnce("cached")
    .mockRejectedValueOnce(new NetworkError())
    .mockResolvedValueOnce("fresh");
  const apply = vi.fn();
  const { result } = renderHook(() => useBackgroundRefresh(load, apply));
  await act(() => result.current.refresh());
  await act(() => result.current.refresh());
  expect(result.current.error).toContain("网络连接失败");
  expect(apply).toHaveBeenCalledTimes(1);
  expect(apply).toHaveBeenLastCalledWith("cached");
  await act(() => result.current.refresh());
  expect(result.current.error).toBe("");
  expect(apply).toHaveBeenLastCalledWith("fresh");
});
it("旧请求在新请求成功后失败也不能把错误重新留在页面", async () => {
  let reject!: (e: Error) => void;
  let oldSignal: AbortSignal | undefined;
  const load = vi
    .fn()
    .mockImplementationOnce((signal: AbortSignal) => {
      oldSignal = signal;
      return new Promise<string>((_r, j) => {
        reject = j;
      });
    })
    .mockResolvedValueOnce("new");
  const apply = vi.fn();
  const { result } = renderHook(() => useBackgroundRefresh(load, apply));
  const old = result.current.refresh();
  result.current.stop();
  await act(() => result.current.refresh());
  expect(oldSignal?.aborted).toBe(true);
  await act(async () => {
    reject(new Error("Failed to fetch"));
    await old;
  });
  expect(result.current.error).toBe("");
  expect(apply).toHaveBeenCalledTimes(1);
  expect(apply).toHaveBeenLastCalledWith("new");
});
it("取消或卸载后的请求不产生提示，也不能应用旧数据", async () => {
  let resolve!: (v: string) => void;
  const apply = vi.fn();
  const load = () =>
    new Promise<string>((r) => {
      resolve = r;
    });
  const { result, unmount } = renderHook(() =>
    useBackgroundRefresh(load, apply),
  );
  const pending = result.current.refresh();
  unmount();
  resolve("late");
  await pending;
  expect(apply).not.toHaveBeenCalled();
});
it("请求取消不会变成网络错误", async () => {
  const { result } = renderHook(() =>
    useBackgroundRefresh(async () => {
      throw new DOMException("cancelled", "AbortError");
    }, vi.fn()),
  );
  await act(() => result.current.refresh());
  expect(result.current.error).toBe("");
});

it("并发刷新合并为一次后续读取，不打断慢请求或无限延长调用等待", async () => {
  let release!: (value: string) => void;
  let signal: AbortSignal | undefined;
  const load = vi
    .fn()
    .mockImplementationOnce((s: AbortSignal) => {
      signal = s;
      return new Promise<string>((r) => {
        release = r;
      });
    })
    .mockResolvedValue("fresh");
  const apply = vi.fn();
  const { result } = renderHook(() => useBackgroundRefresh(load, apply));
  const first = result.current.refresh();
  const second = result.current.refresh();
  const third = result.current.refresh();
  expect(second).toBe(third);
  expect(load).toHaveBeenCalledTimes(1);
  expect(signal?.aborted).toBe(false);
  await act(async () => {
    release("older");
    await Promise.all([first, second, third]);
  });
  expect(load).toHaveBeenCalledTimes(2);
  expect(apply.mock.calls).toEqual([["older"], ["fresh"]]);
  expect(result.current.error).toBe("");
});

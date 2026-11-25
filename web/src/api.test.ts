import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
afterEach(() => vi.unstubAllGlobals());
describe("请求取消与响应解析", () => {
  it("解析响应期间取消时保留 AbortError，不返回伪造成功数据", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => {
          throw new DOMException("已取消", "AbortError");
        },
      }),
    );
    await expect(api("/messages")).rejects.toMatchObject({
      name: "AbortError",
    });
  });
  it("成功状态的无效 JSON 必须报错，不能污染调用方状态", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => {
          throw new SyntaxError("invalid JSON");
        },
      }),
    );
    await expect(api("/messages")).rejects.toThrow("服务响应无效");
  });
  it("响应已经解析后仍拒绝已取消的请求", async () => {
    const controller = new AbortController();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        json: async () => {
          controller.abort();
          return { items: [] };
        },
      }),
    );
    await expect(
      api("/messages", "GET", undefined, controller.signal),
    ).rejects.toMatchObject({ name: "AbortError" });
  });
});

it("fetch 网络错误使用可读提示，并且不自动重试写请求", async () => {
  const fetcher = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
  vi.stubGlobal("fetch", fetcher);
  await expect(
    api("/drafts/d/send", "POST", { key: "send-key" }),
  ).rejects.toMatchObject({
    name: "NetworkError",
    message: "网络连接失败，请检查网络或稍后重试",
  });
  expect(fetcher).toHaveBeenCalledTimes(1);
});
it("fetch 被取消时仍保留 AbortError", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockRejectedValue(new DOMException("cancelled", "AbortError")),
  );
  await expect(api("/bootstrap")).rejects.toMatchObject({ name: "AbortError" });
});

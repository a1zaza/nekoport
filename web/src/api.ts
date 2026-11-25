export class NetworkError extends Error {
  constructor() {
    super("网络连接失败，请检查网络或稍后重试");
    this.name = "NetworkError";
  }
}
export function isAbortError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    "name" in error &&
    error.name === "AbortError"
  );
}
let csrf = "";
export function setCSRF(value: string) {
  csrf = value;
}
export async function api<T>(
  path: string,
  method = "GET",
  value?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const form = value instanceof FormData;
  let response: Response;
  try {
    response = await fetch(`/api/v1${path}`, {
      method,
      credentials: "same-origin",
      signal,
      headers: {
        ...(!form && value !== undefined
          ? { "Content-Type": "application/json" }
          : {}),
        ...(method !== "GET" ? { "X-CSRF-Token": csrf } : {}),
      },
      body:
        value === undefined ? undefined : form ? value : JSON.stringify(value),
    });
  } catch (error) {
    if (signal?.aborted || isAbortError(error))
      throw new DOMException("请求已取消", "AbortError");
    throw new NetworkError();
  }
  if (response.status === 401)
    window.dispatchEvent(new Event("session-expired"));
  let result;
  try {
    result = await response.json();
  } catch (error) {
    if (signal?.aborted || isAbortError(error)) {
      throw new DOMException("请求已取消", "AbortError");
    }
    if (error instanceof TypeError) throw new NetworkError();
    throw new Error(
      response.ok ? "服务响应无效" : `请求失败（HTTP ${response.status}）`,
    );
  }
  if (signal?.aborted) throw new DOMException("请求已取消", "AbortError");
  if (!response.ok) throw new Error(result?.message || "请求失败");
  return result as T;
}
export const download = (path: string) => `/api/v1${path}`;
export function size(bytes: number) {
  return bytes < 1024
    ? `${bytes} B`
    : bytes < 1048576
      ? `${(bytes / 1024).toFixed(1)} KiB`
      : `${(bytes / 1048576).toFixed(1)} MiB`;
}
export function time(value: string, full = false) {
  if (!value) return "尚未同步";
  const date = new Date(value);
  return full
    ? date.toLocaleString("zh-CN")
    : date.toDateString() === new Date().toDateString()
      ? date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" })
      : date.toLocaleDateString("zh-CN", { month: "2-digit", day: "2-digit" });
}

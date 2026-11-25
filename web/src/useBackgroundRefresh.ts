import { useCallback, useEffect, useRef, useState } from "react";
import { isAbortError } from "./api";

type PendingRefresh = {
  controller: AbortController;
  promise: Promise<void>;
  followup: Promise<void> | null;
};

// Refresh failures describe stale displayed data, not failed user mutations.
// Serialize reads and merge bursts into one trailing refresh. This avoids both
// out-of-order errors and starving a slow read by repeatedly aborting it on SSE.
export function useBackgroundRefresh<T>(
  load: (signal: AbortSignal) => Promise<T>,
  apply: (value: T) => void,
) {
  const [error, setError] = useState("");
  const handlers = useRef({ load, apply });
  handlers.current = { load, apply };
  const generation = useRef(0);
  const pending = useRef<PendingRefresh | null>(null);
  const stop = useCallback(() => {
    generation.current++;
    pending.current?.controller.abort();
    pending.current = null;
  }, []);
  const clearError = useCallback(() => setError(""), []);
  const refresh = useCallback((): Promise<void> => {
    const epoch = generation.current;
    function run(): Promise<void> {
      const request: PendingRefresh = {
        controller: new AbortController(),
        promise: Promise.resolve(),
        followup: null,
      };
      pending.current = request;
      request.promise = (async () => {
        try {
          const value = await handlers.current.load(request.controller.signal);
          if (request.controller.signal.aborted || epoch !== generation.current)
            return;
          handlers.current.apply(value);
          setError("");
        } catch (e) {
          if (
            request.controller.signal.aborted ||
            epoch !== generation.current ||
            isAbortError(e)
          )
            return;
          setError(e instanceof Error ? e.message : "数据刷新失败");
        } finally {
          if (pending.current === request) pending.current = null;
        }
      })();
      return request.promise;
    }
    const current = pending.current;
    if (!current) return run();
    if (!current.followup) {
      current.followup = current.promise.then(() => {
        if (epoch !== generation.current) return;
        if (pending.current) return pending.current.promise;
        return run();
      });
    }
    return current.followup;
  }, []);
  useEffect(() => () => stop(), [stop]);
  return { refresh, error, clearError, stop };
}

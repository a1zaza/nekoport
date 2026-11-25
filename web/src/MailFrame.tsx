import { useEffect, useRef } from "react";

// Keep scripts disabled. Same-origin access is used only by the parent to
// measure the sanitized document; never combine this with allow-scripts.
export function MailFrame({ html }: { html: string }) {
  const frame = useRef<HTMLIFrameElement>(null);
  const cleanup = useRef<(() => void) | undefined>(undefined);
  useEffect(() => () => cleanup.current?.(), [html]);

  function observeContent() {
    cleanup.current?.();
    const el = frame.current;
    const doc = el?.contentDocument;
    if (!el || !doc?.body) return;
    let pending = 0;
    function measure() {
      pending = 0;
      if (!el || !doc?.body) return;
      const body = doc.body;
      const rect = body.getBoundingClientRect();
      const margin =
        parseFloat(doc.defaultView!.getComputedStyle(body).marginBottom) || 0;
      const height = Math.max(
        1,
        Math.ceil(
          Math.max(
            doc.documentElement.offsetHeight,
            body.scrollHeight + rect.top + margin,
            rect.bottom + margin,
          ),
        ),
      );
      if (el.style.height !== `${height}px`) el.style.height = `${height}px`;
    }
    function schedule() {
      if (!pending) pending = requestAnimationFrame(measure);
    }
    const observer = new ResizeObserver(schedule);
    observer.observe(doc.documentElement);
    observer.observe(doc.body);
    doc.addEventListener("load", schedule, true);
    doc.defaultView?.addEventListener("resize", schedule);
    cleanup.current = () => {
      observer.disconnect();
      cancelAnimationFrame(pending);
      doc.removeEventListener("load", schedule, true);
      doc.defaultView?.removeEventListener("resize", schedule);
    };
    measure();
  }

  return (
    <iframe
      ref={frame}
      title="邮件 HTML 正文"
      className="mail-frame"
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      srcDoc={html}
      onLoad={observeContent}
    />
  );
}

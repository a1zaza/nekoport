import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { X, ChevronDown, Check, Mail } from "lucide-react";
export function Modal({
  title,
  description,
  children,
  onClose,
  wide = false,
}: {
  title: string;
  description?: string;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const el = ref.current;
    el?.focus();
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        close.current();
      }
      if (event.key === "Tab" && el) {
        const items = Array.from(
          el.querySelectorAll<HTMLElement>(
            'button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),a[href],summary,[tabindex="0"]',
          ),
        );
        if (!items.length) {
          event.preventDefault();
          return;
        }
        const first = items[0],
          last = items[items.length - 1];
        if (
          event.shiftKey &&
          (document.activeElement === first || document.activeElement === el)
        ) {
          event.preventDefault();
          last.focus();
        } else if (
          !event.shiftKey &&
          (document.activeElement === last || document.activeElement === el)
        ) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener("keydown", key);
    return () => {
      document.removeEventListener("keydown", key);
      previous?.focus();
    };
  }, []);
  return (
    <div className="modal-mask">
      <div
        ref={ref}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="dialog-title"
        className={`modal ${wide ? "wide" : ""}`}
      >
        <header className="modal-header">
          <div>
            <h2 id="dialog-title">{title}</h2>
            {description && <p className="muted">{description}</p>}
          </div>
          <button
            className="icon-button"
            aria-label="关闭弹窗"
            onClick={onClose}
          >
            <X size={18} />
          </button>
        </header>
        {children}
      </div>
    </div>
  );
}
export function Select({
  value,
  onChange,
  options,
  label,
  disabled = false,
}: {
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  label: string;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!open) return;
    const anchor = button.current,
      popup = menu.current;
    if (!anchor || !popup) return;
    // Keep the menu in the modal's DOM for focus trapping, but render in the
    // top layer so overflow/transform on any ancestor cannot clip the options.
    popup.showPopover?.();
    const viewport = window.visualViewport;
    const update = () => {
      const rect = anchor.getBoundingClientRect();
      const edge = 8,
        gap = 4;
      const leftEdge = (viewport?.offsetLeft || 0) + edge;
      const topEdge = (viewport?.offsetTop || 0) + edge;
      const rightEdge =
        (viewport?.offsetLeft || 0) +
        (viewport?.width || window.innerWidth) -
        edge;
      const bottomEdge =
        (viewport?.offsetTop || 0) +
        (viewport?.height || window.innerHeight) -
        edge;
      const width = Math.min(
        Math.max(rect.width, 140),
        Math.max(1, rightEdge - leftEdge),
      );
      popup.style.width = `${width}px`;
      const desired = Math.min(220, popup.scrollHeight + 2);
      const below = Math.max(0, bottomEdge - rect.bottom - gap),
        above = Math.max(0, rect.top - topEdge - gap);
      const upwards = below < desired && above > below;
      const height = Math.min(desired, upwards ? above : below);
      popup.style.maxHeight = `${height}px`;
      popup.style.left = `${Math.max(leftEdge, Math.min(rect.left, rightEdge - width))}px`;
      popup.style.top = `${upwards ? rect.top - gap - height : rect.bottom + gap}px`;
      popup.dataset.placement = upwards ? "top" : "bottom";
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(anchor);
    observer.observe(popup);
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    viewport?.addEventListener("resize", update);
    viewport?.addEventListener("scroll", update);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
      viewport?.removeEventListener("resize", update);
      viewport?.removeEventListener("scroll", update);
      if (popup.isConnected) popup.hidePopover?.();
    };
  }, [open]);
  useEffect(() => {
    const click = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", click);
    return () => document.removeEventListener("mousedown", click);
  }, []);
  return (
    <div className="select" ref={root}>
      <button
        ref={button}
        disabled={disabled}
        type="button"
        aria-label={label}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="select-trigger"
        onClick={() => setOpen(!open)}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setOpen(true);
            setTimeout(
              () =>
                root.current
                  ?.querySelector<HTMLButtonElement>("[role=option]")
                  ?.focus(),
              0,
            );
          }
          if (e.key === "Escape" && open) {
            e.stopPropagation();
            setOpen(false);
          }
        }}
      >
        {options.find((o) => o.value === value)?.label || "请选择"}
        <ChevronDown size={14} />
      </button>
      {open && (
        <div
          ref={menu}
          popover="manual"
          className="select-options"
          role="listbox"
          aria-label={label}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.stopPropagation();
              setOpen(false);
              button.current?.focus();
            }
            if (e.key === "ArrowDown" || e.key === "ArrowUp") {
              e.preventDefault();
              const options = Array.from(
                root.current?.querySelectorAll<HTMLButtonElement>(
                  "[role=option]",
                ) || [],
              );
              const i = options.indexOf(
                document.activeElement as HTMLButtonElement,
              );
              options[
                (i + (e.key === "ArrowDown" ? 1 : -1) + options.length) %
                  options.length
              ]?.focus();
            }
          }}
        >
          {options.map((o) => (
            <button
              key={o.value}
              type="button"
              role="option"
              aria-selected={o.value === value}
              onClick={() => {
                onChange(o.value);
                setOpen(false);
                button.current?.focus();
              }}
            >
              {o.label}
              {o.value === value && <Check size={14} />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint && <small className="muted">{hint}</small>}
    </label>
  );
}
export function ErrorBox({ error }: { error: string }) {
  return error ? (
    <div className="form-error" role="alert">
      {error}
    </div>
  ) : null;
}
export function Empty({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty">
      <div className="empty-symbol">
        <Mail size={30} strokeWidth={1.3} />
      </div>
      <h2>{title}</h2>
      <p>{description}</p>
      {children}
    </div>
  );
}

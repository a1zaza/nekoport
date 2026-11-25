import DOMPurify from "dompurify";
import { sanitizeMailStyles } from "./mail-styles";

export function mailDocument(
  source: string,
  remote = false,
  fontSize = 15,
  cidImages: Record<string, string> = {},
): string {
  const readingSize = Number.isFinite(fontSize)
    ? Math.max(12, Math.min(24, fontSize))
    : 15;
  const clean = DOMPurify.sanitize(
    new DOMParser().parseFromString(source, "text/html").documentElement
      .outerHTML,
    {
      FORBID_TAGS: [
        "script",
        "form",
        "input",
        "button",
        "textarea",
        "select",
        "object",
        "embed",
        "iframe",
        "svg",
        "math",
        "video",
        "audio",
        "link",
        "meta",
        "base",
      ],
      FORBID_ATTR: ["srcset", "background", "action", "formaction", "ping"],
      ALLOW_DATA_ATTR: false,
      ADD_TAGS: ["style", "header", "footer", "main"],
      ADD_ATTR: ["text", "link", "alink", "vlink"],
      WHOLE_DOCUMENT: true,
    },
  );
  const doc = new DOMParser().parseFromString(clean, "text/html");
  const mailCSS = sanitizeMailStyles(doc);
  doc.querySelectorAll("img").forEach((img) => {
    const src = img.getAttribute("src") || "";
    const cid = src.toLowerCase().startsWith("cid:")
      ? cidImages[src.slice(4)]
      : undefined;
    if (cid?.startsWith("blob:")) {
      img.setAttribute("src", cid);
      return;
    }
    if (!remote || !/^https?:\/\//i.test(src)) {
      img.removeAttribute("src");
      img.setAttribute("alt", img.getAttribute("alt") || "[图片已拦截]");
    }
  });
  doc.querySelectorAll("a").forEach((link) => {
    const href = link.getAttribute("href") || "";
    if (!/^(https?:|mailto:)/i.test(href)) link.removeAttribute("href");
    link.setAttribute("target", "_blank");
    link.setAttribute("rel", "noopener noreferrer");
  });
  // Center the email's own outer layout boxes without changing their widths,
  // inner spacing, display mode or text alignment.
  const outerBoxes = [
    doc.documentElement,
    doc.body,
    ...Array.from(doc.body.children).filter((el) =>
      /^(DIV|TABLE|MAIN|ARTICLE|SECTION|HEADER|FOOTER)$/.test(el.tagName),
    ),
  ];
  outerBoxes.forEach((el) => {
    const box = el as HTMLElement;
    box.style.setProperty("margin-left", "auto", "important");
    box.style.setProperty("margin-right", "auto", "important");
  });
  const images = remote ? "blob: https: http:" : "blob:";
  doc.documentElement.setAttribute("lang", "zh-CN");
  doc.head.innerHTML = `<meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; img-src ${images}; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"><style>
html{color-scheme:light}
body{font-size:${readingSize}px}
img{max-width:100%;height:auto}
${mailCSS}
html{overflow-y:hidden!important}
</style>`;
  return `<!doctype html>${doc.documentElement.outerHTML}`;
}

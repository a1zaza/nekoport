import { describe, it, expect } from "vitest";
import { mailDocument } from "./html";
describe("邮件 HTML 隔离", () => {
  it("默认阻止外部请求、脚本、表单和样式 URL", () => {
    const result = mailDocument(
      '<script>evil()</script><form action="https://evil.test"><input></form><p style="background:url(https://tracker.test)">你好</p><img src="https://tracker.test/pixel" onerror="evil()"><a href="javascript:evil()">链接</a>',
    );
    const doc = new DOMParser().parseFromString(result, "text/html");
    expect(doc.querySelector("script,form,input")).toBeNull();
    expect(doc.querySelector("img")?.hasAttribute("src")).toBe(false);
    expect(doc.querySelector("p")?.getAttribute("style") || "").not.toContain(
      "url",
    );
    expect(doc.querySelector("a")?.hasAttribute("href")).toBe(false);
    expect(doc.body.textContent).toContain("你好");
    expect(result).toContain("default-src 'none'");
    expect(result).toContain("script-src 'none'");
  });
  it("单封授权仅放行 http 图片", () => {
    const doc = new DOMParser().parseFromString(
      mailDocument(
        '<img src="https://example.test/image"><img src="cid:secret"><a href="https://example.test">链接</a>',
        true,
      ),
      "text/html",
    );
    expect(doc.querySelectorAll("img")[0].getAttribute("src")).toBe(
      "https://example.test/image",
    );
    expect(doc.querySelectorAll("img")[1].hasAttribute("src")).toBe(false);
    expect(doc.querySelector("a")?.getAttribute("rel")).toBe(
      "noopener noreferrer",
    );
  });
});

describe("邮件原始配色", () => {
  it("保留根元素及内部背景和文字色，不注入平台配色", () => {
    const doc = new DOMParser().parseFromString(
      mailDocument(
        `<html style="background-color:#f0f0f0"><head><style>body{background:#fdf6e3;color:#123456}a{color:#ab1234}</style></head><body bgcolor="#fdf6e3" text="#123456" style="background-color:#fdf6e3;color:#123456;margin:0"><table bgcolor="white"><tr><td><font color="black">正文</font></td></tr></table></body></html>`,
      ),
      "text/html",
    );
    expect(doc.documentElement.style.backgroundColor).toBe(
      "rgb(240, 240, 240)",
    );
    expect(doc.body.style.backgroundColor).toBe("rgb(253, 246, 227)");
    expect(doc.body.style.color).toBe("rgb(18, 52, 86)");
    expect(doc.body.getAttribute("bgcolor")).toBe("#fdf6e3");
    expect(doc.body.getAttribute("text")).toBe("#123456");
    expect(doc.querySelector("table")?.getAttribute("bgcolor")).toBe("white");
    expect(doc.querySelector("style")?.textContent).toContain(
      "background:#fdf6e3",
    );
    expect(doc.querySelector("style")?.textContent).not.toMatch(
      /#181818|#d4d4d4/,
    );
  });
  it("没有自带样式时采用浏览器浅色默认，并安全回退无效字号", () => {
    const doc = new DOMParser().parseFromString(
      mailDocument("<p>正文</p>", false, NaN),
      "text/html",
    );
    expect(doc.querySelector("style")?.textContent).toContain(
      "color-scheme:light",
    );
    expect(doc.querySelector("style")?.textContent).toContain("font-size:15px");
  });
});

it("保留安全排版、类样式和媒体查询，拒绝 CSS 外部资源与定位", () => {
  const result = mailDocument(
    `<html><head><style>@import url(https://tracker.test/css); .card{padding:24px;color:#234;background-image:url(https://tracker.test/pixel)} @media (max-width:600px){.card{width:100%}}</style></head><body><table class="card" style="font-size:18px;padding:12px;position:fixed;background-image:url(https://tracker.test/bg)"><tr><td>正文</td></tr></table></body></html>`,
  );
  const doc = new DOMParser().parseFromString(result, "text/html");
  expect(doc.querySelector("table")?.style.fontSize).toBe("18px");
  expect(doc.querySelector("table")?.style.padding).toBe("12px");
  expect(doc.querySelector("style")?.textContent).toContain("padding:24px");
  expect(doc.querySelector("style")?.textContent).toMatch(
    /color:(#234|rgb\(34, 51, 68\))/,
  );
  expect(result).toMatch(/@media \(max-width:\s*600px\)/);
  expect(result).not.toMatch(
    /tracker\.test|@import|position:fixed|background-image/,
  );
});

it("保留现代排版、中文字体和条件样式，保持资源与脚本隔离", () => {
  const doc = new DOMParser().parseFromString(
    mailDocument(
      `<style>.layout{display:grid;grid-template-columns:[start] 1fr 2fr;gap:16px;position:relative;overflow:hidden;box-shadow:0 2px 4px #0003;font-family:'霞鹜臻楷',sans-serif}@supports (display:grid){.layout{align-items:center}}.child{position:absolute;top:8px;transform:translateX(2px);opacity:.8}.bad{position:fixed;background:url(https://tracker.test);font-family:var(--font)}</style><main><table cellpadding="12" cellspacing="0"><tr><td valign="top" nowrap>正文</td></tr></table><div style="display:flex;flex-wrap:wrap;gap:12px;font-family:'霞鹜臻楷';position:relative" onclick="evil()">内容</div><script>evil()</script>`,
    ),
    "text/html",
  );
  const css = doc.querySelector("style")!.textContent!;
  for (const value of [
    "grid-template-columns",
    "gap",
    "position:relative",
    "overflow:hidden",
    "box-shadow",
    "霞鹜臻楷",
    "@supports",
    "position:absolute",
    "transform",
    "opacity",
  ])
    expect(css).toContain(value);
  const div = doc.querySelector("div")!;
  expect(div.style.flexWrap).toBe("wrap");
  expect(div.style.fontFamily).toContain("霞鹜臻楷");
  expect(div.style.position).toBe("relative");
  expect(doc.querySelector("td")!.getAttribute("valign")).toBe("top");
  expect(doc.querySelector("table")!.getAttribute("cellpadding")).toBe("12");
  expect(doc.querySelector("main")).not.toBeNull();
  expect(doc.querySelector("script,[onclick]")).toBeNull();
  expect(css).not.toMatch(/fixed|tracker\.test|var\(/);
});

it("采用邮件自身宽度并居中外层布局，保持文字和内部间距", () => {
  const doc = new DOMParser().parseFromString(
    mailDocument(
      `<html style="max-width:1100px"><body style="max-width:940px;margin:0!important;text-align:left"><style>.mail-root{width:100%;max-width:820px}</style><div class="mail-root" style="margin:0!important;text-align:right"><p style="margin-left:20px">正文</p></div><table width="760" style="max-width:760px;margin-left:0!important"><tr><td>表格</td></tr></table></body></html>`,
    ),
    "text/html",
  );
  expect(doc.documentElement.style.maxWidth).toBe("1100px");
  expect(doc.body.style.maxWidth).toBe("940px");
  expect(doc.body.style.textAlign).toBe("left");
  expect(doc.querySelector(".mail-root")?.getAttribute("style")).toContain(
    "text-align: right",
  );
  expect(doc.querySelector("style")?.textContent).toContain("max-width:820px");
  expect(doc.querySelector("table")?.getAttribute("width")).toBe("760");
  for (const el of [
    doc.documentElement,
    doc.body,
    doc.querySelector(".mail-root")!,
    doc.querySelector("table")!,
  ]) {
    const style = (el as HTMLElement).style;
    expect(style.marginLeft).toBe("auto");
    expect(style.marginRight).toBe("auto");
  }
  expect(doc.querySelector("p")?.style.marginLeft).toBe("20px");
  expect(doc.querySelector("style")?.textContent).not.toContain(
    "max-width:600px",
  );
});

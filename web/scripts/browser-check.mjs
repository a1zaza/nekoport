// A self-contained QA server serves built assets or source and mocks APIs only.
// It never opens real mailbox connections or starts the development environment.
import { createServer } from "node:http";
import { access, readFile, readdir } from "node:fs/promises";
import { resolve, extname, dirname, basename, relative } from "node:path";
import { chromium } from "playwright-core";
import assert from "node:assert/strict";
const dist = resolve("dist");
let sourceServer;
const server = createServer(async (req, res) => {
  try {
    res.setHeader(
      "Content-Security-Policy",
      "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https: http:; frame-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
    );
    if (sourceServer) {
      sourceServer.middlewares(req, res, () => {
        res.writeHead(404);
        res.end();
      });
      return;
    }
    const path = new URL(req.url, "http://localhost").pathname;
    const file = resolve(dist, "." + (path === "/" ? "/index.html" : path));
    if (!file.startsWith(dist + "/")) throw Error();
    const body = await readFile(file);
    res.setHeader(
      "Content-Type",
      {
        ".html": "text/html",
        ".js": "text/javascript",
        ".css": "text/css",
        ".svg": "image/svg+xml",
      }[extname(file)] || "application/octet-stream",
    );
    res.end(body);
  } catch {
    res.writeHead(404);
    res.end();
  }
});
// Reuse an installed browser without tying the repository to one user's home
// directory or forcing a browser download when the tool version changes.
async function existingChromium() {
  if (process.env.NEKOPORT_CHROMIUM) return process.env.NEKOPORT_CHROMIUM;
  const preferred = chromium.executablePath();
  const exists = async (path) => {
    try {
      await access(path);
      return true;
    } catch {
      return false;
    }
  };
  if (await exists(preferred)) return preferred;
  let revision = dirname(preferred);
  while (
    revision !== dirname(revision) &&
    !/^chromium-\d+$/.test(basename(revision))
  )
    revision = dirname(revision);
  if (/^chromium-\d+$/.test(basename(revision))) {
    const cache = dirname(revision),
      suffix = relative(revision, preferred);
    const entries = await readdir(cache, { withFileTypes: true }).catch(
      () => [],
    );
    const versions = entries
      .filter(
        (entry) => entry.isDirectory() && /^chromium-\d+$/.test(entry.name),
      )
      .map((entry) => entry.name)
      .sort((a, b) => b.localeCompare(a, "en", { numeric: true }));
    for (const version of versions) {
      const path = resolve(cache, version, suffix);
      if (await exists(path)) return path;
    }
  }
  throw new Error(
    "未找到已安装的 Chromium，请通过 NEKOPORT_CHROMIUM 指定浏览器路径。",
  );
}
const executablePath = await existingChromium();
if (process.env.NEKOPORT_UI_SOURCE === "1" && !process.env.NEKOPORT_UI_URL) {
  const { createServer: createViteServer } = await import("vite");
  sourceServer = await createViteServer({
    configFile: false,
    root: resolve("."),
    appType: "spa",
    server: { middlewareMode: true, hmr: false, watch: null },
  });
}
await new Promise((ok, fail) => {
  server.once("error", fail);
  server.listen(0, "127.0.0.1", ok);
});
const browser = await chromium
  .launch({
    executablePath,
    headless: true,
    args: ["--no-sandbox"],
  })
  .catch(async (error) => {
    await new Promise((ok) => server.close(ok));
    await sourceServer?.close();
    throw error;
  });
const endpoint = {
  host: "mail.example.test",
  port: 993,
  tls: "tls",
  username: "test",
};
const account = {
  id: "a",
  name: "工作邮箱",
  address: "test@example.test",
  imap: endpoint,
  smtp: { ...endpoint, port: 465 },
  credentialRef: "",
  paused: false,
  syncDays: 30,
  autoArchive: false,
  revision: 1,
};
const plainBody =
  "\n  第一行保留两个空格\n\t制表符缩进\n<script>字面文本</script>\n" +
  "https://example.test/" +
  "very-long-path".repeat(60);
const msg = {
  id: "m",
  accountId: "a",
  mailboxId: "inbox",
  uid: 1,
  uidValidity: 1,
  messageId: "<m@test>",
  from: '"项目团队" <team@example.test>',
  fromName: "项目团队",
  fromDisplay: "项目团队 <team@example.test>",
  toDisplay: "test@example.test",
  to: "test@example.test",
  cc: "",
  subject: "项目进展与下周安排",
  date: new Date().toISOString(),
  preview: "新的资料已经整理完毕，请查阅附件。",
  seen: false,
  starred: true,
  hasAttachments: true,
  revision: 1,
};
let authenticated = true;
let authRevision = 1;
let username = "test-user";
let syncTasks = [];
let emptyOverview = false;
let extraAccount = true;
let failedTaskReads = 0;
let missingArchive = false;
const mailOperations = [];
let listMessages = null;
let rejectOperation = false;
let holdOperation = null;
let removeOperationTargets = false;
let failedBootstrap = 0,
  failedMessages = 0,
  rejectSync = false;
let draft = null,
  body = "",
  savedCount = 0,
  sentCount = 0,
  remoteRequests = 0;
try {
  const context = await browser.newContext();
  await context.addInitScript(() => {
    window.EventSource = class {
      constructor() {
        setTimeout(() => this.onopen?.(), 0);
      }
      addEventListener() {}
      close() {}
    };
  });
  const page = await context.newPage();
  const errors = [];

  page.on("pageerror", (e) => {
    errors.push(e.message);
    console.error("浏览器异常：", e.stack);
  });
  async function checkAppFrame() {
    const layout = await page.locator(".app-shell").evaluate((el) => {
      const rect = el.getBoundingClientRect(),
        style = getComputedStyle(el);
      return {
        x: rect.x,
        y: rect.y,
        width: rect.width,
        height: rect.height,
        border: style.borderTopWidth,
        color: style.borderTopColor,
        radius: style.borderRadius,
        shadow: style.boxShadow,
        viewportWidth: innerWidth,
        viewportHeight: innerHeight,
        noOverflow:
          document.documentElement.scrollWidth <= innerWidth &&
          document.documentElement.scrollHeight <= innerHeight,
      };
    });
    const mobile = layout.viewportWidth <= 720,
      inset = mobile ? 0 : 20;
    assert.equal(layout.x, inset);
    assert.equal(layout.y, inset);
    assert.equal(layout.width, layout.viewportWidth - inset * 2);
    assert.equal(layout.height, layout.viewportHeight - inset * 2);
    assert.equal(layout.border, mobile ? "0px" : "1px");
    assert.equal(layout.radius, mobile ? "0px" : "10px");
    if (!mobile) assert.equal(layout.color, "rgb(53, 53, 53)");
    assert.equal(layout.shadow === "none", mobile);
    assert.ok(layout.noOverflow, "outer frame causes page overflow");
  }
  async function checkMoveMenu(flip = false) {
    await page
      .locator(".reader-toolbar")
      .getByRole("button", { name: "移动邮件", exact: true })
      .click();
    const dialog = page.getByRole("dialog", {
      name: "移动到文件夹",
      exact: true,
    });
    if (flip)
      await dialog.evaluate((el) => {
        el.style.transform = "translateY(220px)";
      });
    await dialog
      .getByRole("button", { name: "目标文件夹", exact: true })
      .click();
    const list = page.getByRole("listbox", { name: "目标文件夹", exact: true });
    await list.waitFor();
    if (flip) assert.equal(await list.getAttribute("data-placement"), "top");
    const bounds = await list.boundingBox(),
      viewport = page.viewportSize();
    assert.ok(
      bounds.x >= 0 &&
        bounds.y >= 0 &&
        bounds.x + bounds.width <= viewport.width &&
        bounds.y + bounds.height <= viewport.height,
    );
    const last = list.getByRole("option", { name: "项目 10", exact: true });
    await last.scrollIntoViewIfNeeded();
    assert.ok(
      await last.evaluate((el) => {
        const r = el.getBoundingClientRect();
        const hit = document.elementFromPoint(
          r.x + r.width / 2,
          r.y + r.height / 2,
        );
        return hit === el || el.contains(hit);
      }),
      "last folder is clipped or blocked",
    );
    await last.click();
    assert.ok(
      (
        await dialog
          .getByRole("button", { name: "目标文件夹", exact: true })
          .innerText()
      ).includes("项目 10"),
    );
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "hidden" });
  }
  await page.route("https://tracker.example.test/**", (route) => {
    remoteRequests++;
    return route.fulfill({
      contentType: "image/png",
      body: Buffer.from(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+k5iQAAAAASUVORK5CYII=",
        "base64",
      ),
    });
  });
  await page.route("**/api/v1/**", async (route) => {
    const req = route.request(),
      path = new URL(req.url()).pathname;
    let result = { ok: true };
    if (path.endsWith("/bootstrap") && failedBootstrap > 0) {
      failedBootstrap--;
      return route.abort("failed");
    }
    if (
      path.endsWith("/sync-tasks") &&
      req.method() === "GET" &&
      failedTaskReads > 0
    ) {
      failedTaskReads--;
      return route.abort("failed");
    }
    if (path.endsWith("/messages") && failedMessages > 0) {
      failedMessages--;
      return route.abort("failed");
    }
    if (path.endsWith("/accounts/a/sync") && rejectSync)
      return route.fulfill({
        status: 409,
        contentType: "application/json",
        body: JSON.stringify({ message: "同步操作失败，请检查邮箱配置" }),
      });

    if (path.endsWith("/session"))
      result = { authenticated, csrf: "test-csrf" };
    else if (path.endsWith("/events"))
      return route.fulfill({
        contentType: "text/event-stream",
        body: ": keepalive\n\n",
      });
    else if (path.endsWith("/bootstrap"))
      result = {
        authAccount: { username, revision: authRevision },
        syncTasks: syncTasks.filter((t) =>
          ["queued", "running"].includes(t.state),
        ),
        syncStatus: {
          a: { nextDueAt: new Date(Date.now() + 900000).toISOString() },
        },
        accounts: [account],
        mailboxes: [
          {
            id: "inbox",
            accountId: "a",
            name: "INBOX",
            role: "inbox",
            unread: 1,
          },
          { id: "sent", accountId: "a", name: "Sent", role: "sent", unread: 0 },
          {
            id: "trash",
            accountId: "a",
            name: "Trash",
            role: "trash",
            unread: 0,
          },
          {
            id: "custom10",
            accountId: "a",
            name: "项目 10",
            role: "folder",
            unread: 0,
          },
          { id: "junk", accountId: "a", name: "Junk", role: "junk", unread: 0 },
          {
            id: "archive",
            accountId: "a",
            name: "Archive",
            role: "archive",
            unread: 0,
          },
          {
            id: "server-drafts",
            accountId: "a",
            name: "Drafts",
            role: "drafts",
            unread: 0,
          },
          {
            id: "custom2",
            accountId: "a",
            name: "项目 2",
            role: "folder",
            unread: 0,
          },
        ],
        drafts: draft ? [draft] : [],
        jobs: [
          {
            id: "legacy-job",
            accountId: "a",
            subject: "历史发件显示验证",
            to: `=?UTF-8?B?${Buffer.from("项目团队").toString("base64")}?= <team@example.test>`,
            toDisplay: "项目团队 <team@example.test>",
            fromDisplay: "工作邮箱 <test@example.test>",
            state: "sent",
            archive: "done",
            createdAt: new Date().toISOString(),
          },
        ],
        operations: [],
        settings: { fontSize: 15, autoRead: true, cacheMiB: 1024 },
        running: {},
        total: 1,
      };
    else if (path.endsWith("/message-operations")) {
      if (rejectOperation)
        return route.fulfill({
          status: 409,
          contentType: "application/json",
          body: JSON.stringify({ message: "模拟批量操作失败" }),
        });
      const input = req.postDataJSON();
      mailOperations.push(input);
      if (holdOperation) await holdOperation;
      if (removeOperationTargets && ["move", "trash"].includes(input.kind))
        listMessages = listMessages.filter(
          (message) => !input.ids.includes(message.id),
        );
      result = { ok: true };
    } else if (path.endsWith("/sync-tasks")) {
      if (req.method() === "POST") {
        const input = req.postDataJSON();
        const t = {
          id: `sync-${syncTasks.length + 1}`,
          accountId: "a",
          kind: input.kind,
          state: "queued",
          folders: [],
        };
        syncTasks.unshift(t);
        result = t;
        if (input.kind === "history") {
          account.syncScope = "all";
          account.revision++;
        }
      } else {
        const group = new URL(req.url()).searchParams.get("group");
        result = {
          items: syncTasks.filter((task) =>
            group === "quick"
              ? task.kind === "quick"
              : group === "full"
                ? task.kind !== "quick"
                : true,
          ),
          nextOffset: 0,
        };
      }
    } else if (/\/sync-tasks\/[^/]+\/(cancel|resume)$/.test(path)) {
      const t = syncTasks.find((t) => path.includes(t.id));
      t.state = path.endsWith("/cancel") ? "cancelled" : "queued";
      result = { ok: true };
    } else if (path.endsWith("/sync-settings")) {
      const input = req.postDataJSON();
      account.syncScope = input.scope;
      account.syncDays = input.recentDays;
      account.reconcileEnabled = input.reconcileEnabled;
      account.reconcileIntervalMinutes = input.intervalMinutes;
      account.revision++;
      result = { ok: true };
    } else if (path.endsWith("/auth/account")) {
      const input = req.postDataJSON();
      if (input.currentPassword !== "test-password")
        return route.fulfill({
          status: 403,
          contentType: "application/json",
          body: JSON.stringify({ message: "当前密码不正确" }),
        });
      username = input.username;
      authRevision++;
      authenticated = false;
      result = { reauthenticate: true };
    } else if (path.endsWith("/messages"))
      result = { items: listMessages || [msg], nextCursor: "", partial: false };
    else if (path.endsWith("/messages/m"))
      result = {
        message: msg,
        content: {
          text: plainBody,
          html: '<html><body bgcolor="#fdf6e3" text="#123456" style="background-color:#fdf6e3;color:#123456"><style>.mail-card{max-width:820px;box-sizing:border-box;padding:24px;border:2px solid #456;font-size:18px;display:flex;flex-wrap:wrap;gap:12px;font-family:霞鹜臻楷;position:relative;box-shadow:0 2px 4px #0003}@supports (display:grid){.mail-card{display:grid;grid-template-columns:1fr 2fr}}</style><div class="mail-card" style="font-weight:bold;margin:0!important">样式保留测试</div><h2>项目进展</h2><table bgcolor="white"><tr><td bgcolor="#fff"><font color="black">主题配色测试</font></td></tr></table><p>你好，这是一封邮件。</p><div style="height:1400px">长邮件末尾测试</div><img src="https://tracker.example.test/pixel" onerror="window.hacked=true"><script>window.hacked=true</script>',
          attachments: [],
        },
      };
    else if (path.endsWith("/send")) {
      sentCount++;
      result = { id: "job", state: "queued" };
    } else if (
      path.includes("/drafts") &&
      ["POST", "PUT"].includes(req.method())
    ) {
      const input = req.postDataJSON();
      body = input.body;
      draft = {
        ...input,
        id: "d",
        revision: (draft?.revision || 0) + 1,
        bodyId: "object",
        attachments: [],
        updatedAt: new Date().toISOString(),
      };
      delete draft.body;
      savedCount++;
      result = draft;
    } else if (path.endsWith("/drafts/d")) result = { draft, body };

    if (extraAccount && path.endsWith("/bootstrap")) {
      result.accounts.push({
        ...account,
        id: "b",
        name: "个人邮箱",
        address: "personal@example.test",
      });
      result.mailboxes.push({
        id: "b-inbox",
        accountId: "b",
        name: "INBOX",
        role: "inbox",
        unread: 0,
      });
    }
    if (missingArchive && path.endsWith("/bootstrap"))
      result.mailboxes = result.mailboxes.filter((b) => b.role !== "archive");
    if (emptyOverview && path.endsWith("/bootstrap"))
      result = {
        ...result,
        accounts: [],
        mailboxes: [],
        drafts: [],
        jobs: [],
        operations: [],
        running: {},
        total: 0,
      };
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(result),
    });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(
    process.env.NEKOPORT_UI_URL || `http://127.0.0.1:${server.address().port}`,
  );
  await page.getByRole("heading", { name: "邮件概览", exact: true }).waitFor();
  await page.getByRole("button", { name: /已配置邮箱/ }).waitFor();
  await checkAppFrame();
  // The overlay must remain inside the framed app on tablets and fill phones.
  for (const width of [1100, 799, 721, 720, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await checkAppFrame();
    await page
      .getByRole("button", { name: "展开邮箱导航", exact: true })
      .click();
    await page.waitForFunction(() => {
      const shell = document.querySelector(".app-shell"),
        side = document.querySelector(".sidebar");
      return (
        Math.abs(
          side.getBoundingClientRect().left -
            shell.getBoundingClientRect().left -
            parseFloat(getComputedStyle(shell).borderLeftWidth),
        ) <= 1
      );
    });
    const alignment = await page.evaluate(() => {
      const shell = document
        .querySelector(".app-shell")
        .getBoundingClientRect();
      const top = document.querySelector(".topbar").getBoundingClientRect();
      const side = document.querySelector(".sidebar").getBoundingClientRect();
      const mask = document
        .querySelector(".sidebar-mask")
        .getBoundingClientRect();
      return {
        fits:
          side.left >= shell.left - 1 &&
          side.bottom <= shell.bottom + 1 &&
          mask.left >= shell.left - 1 &&
          mask.right <= shell.right + 1 &&
          mask.bottom <= shell.bottom + 1,
        aligned:
          Math.abs(side.top - top.bottom) <= 1 &&
          Math.abs(mask.top - top.bottom) <= 1,
      };
    });
    assert.ok(
      alignment.fits && alignment.aligned,
      `navigation escapes outer frame at ${width}px`,
    );
    if (width === 1100)
      await page.screenshot({ path: "/tmp/nekoport-framed-tablet.png" });
    const mask = page.getByRole("button", { name: "关闭导航", exact: true });
    const bounds = await mask.boundingBox();
    await mask.click({ position: { x: bounds.width - 12, y: 12 } });
    await page.locator(".sidebar.open").waitFor({ state: "hidden" });
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await checkAppFrame();
  const workAccount = page
    .locator(".account-navigation")
    .getByRole("button", { name: "工作邮箱", exact: true });
  const personalAccount = page
    .locator(".account-navigation")
    .getByRole("button", { name: "个人邮箱", exact: true });
  assert.equal(await page.locator(".folder-list").count(), 0);
  assert.equal(await workAccount.getAttribute("aria-expanded"), "false");
  assert.equal(await personalAccount.getAttribute("aria-expanded"), "false");
  const accountRows = await page
    .locator(".account-title")
    .evaluateAll((elements) =>
      elements.map((el) => ({
        height: el.getBoundingClientRect().height,
        top: el.getBoundingClientRect().top,
        bottom: el.getBoundingClientRect().bottom,
        radius: getComputedStyle(el).borderRadius,
      })),
    );
  const menuHeight = await page
    .locator(".main-nav .nav-item")
    .first()
    .evaluate((el) => el.getBoundingClientRect().height);
  assert.ok(Math.abs(accountRows[1].top - accountRows[0].bottom) <= 1);
  for (const row of accountRows) {
    assert.equal(row.radius, "0px");
    assert.equal(row.height, menuHeight);
  }
  await workAccount.click();
  await page.waitForFunction(() => {
    const title = document.querySelector(
      '.account-title[aria-expanded="true"]',
    );
    return (
      title && getComputedStyle(title).borderLeftColor === "rgb(0, 122, 204)"
    );
  });
  assert.equal(
    await workAccount.evaluate((el) => getComputedStyle(el).borderLeftColor),
    "rgb(0, 122, 204)",
  );
  assert.equal(
    await personalAccount.evaluate((el) => el.classList.contains("selected")),
    false,
  );
  assert.equal(await page.locator(".folder-list").count(), 1);
  assert.equal(await workAccount.getAttribute("aria-expanded"), "true");
  assert.deepEqual(
    await page.locator(".folder-list .folder-item > span").allTextContents(),
    [
      "收件箱",
      "服务器草稿",
      "已发送",
      "归档",
      "垃圾邮件",
      "已删除",
      "项目 2",
      "项目 10",
    ],
  );

  await personalAccount.click();
  assert.equal(await workAccount.getAttribute("aria-expanded"), "false");
  assert.equal(await personalAccount.getAttribute("aria-expanded"), "true");
  assert.equal(await page.locator(".folder-list").count(), 1);
  await personalAccount.click();
  assert.equal(await page.locator(".folder-list").count(), 0);
  await personalAccount.click();
  extraAccount = false;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await personalAccount.waitFor({ state: "hidden" });
  assert.equal(await page.locator(".folder-list").count(), 0);
  await workAccount.click();

  await page.screenshot({ path: "/tmp/nekoport-overview-desktop.png" });
  rejectSync = true;
  await page.getByRole("button", { name: "同步邮箱", exact: true }).click();
  await page
    .locator(".global-error")
    .filter({ hasText: "同步操作失败" })
    .waitFor();
  rejectSync = false;
  failedBootstrap = 1;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page
    .locator(".refresh-warning")
    .filter({ hasText: "网络连接失败" })
    .waitFor();
  assert.ok(
    await page
      .getByRole("heading", { name: "邮件概览", exact: true })
      .isVisible(),
  );
  await page
    .locator(".refresh-warning")
    .getByRole("button", { name: "重试刷新", exact: true })
    .click();
  await page.locator(".refresh-warning").waitFor({ state: "hidden" });
  assert.ok(
    await page
      .locator(".global-error")
      .filter({ hasText: "同步操作失败" })
      .isVisible(),
  );
  await page.getByRole("button", { name: "关闭错误", exact: true }).click();

  await page.getByRole("button", { name: "查看收件箱", exact: true }).click();
  await page.getByRole("heading", { name: "所有收件箱" }).waitFor();
  await page
    .locator(".main-nav")
    .getByRole("button", { name: /发件箱/ })
    .click();
  const jobText = await page.locator(".job-record .record-main").innerText();
  assert.ok(jobText.includes("项目团队 <team@example.test>"));
  assert.ok(jobText.includes("工作邮箱 <test@example.test>"));
  assert.ok(!jobText.includes("=?UTF-8?"));
  await page
    .locator(".main-nav")
    .getByRole("button", { name: /所有收件箱/ })
    .click();
  await page
    .getByRole("heading", { name: "所有收件箱", exact: true })
    .waitFor();

  await page.getByRole("button", { name: /项目团队.*项目进展/ }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  assert.equal(
    await page.locator(".message-row .sender").first().textContent(),
    "项目团队",
  );
  assert.equal(
    await page.locator(".sender-block strong").textContent(),
    "项目团队 <team@example.test>",
  );
  assert.equal(await page.locator(".sender-block .avatar").textContent(), "项");

  failedMessages = 1;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page
    .locator(".refresh-warning")
    .filter({ hasText: "邮件列表暂时无法更新" })
    .waitFor();
  assert.equal(
    await page.locator(".message-row .sender").first().textContent(),
    "项目团队",
  );
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.locator(".refresh-warning").waitFor({ state: "hidden" });
  assert.equal(await page.locator(".global-error").count(), 0);
  await checkMoveMenu();
  await checkMoveMenu(true);
  const platformColors = await page.evaluate(() => ({
    background: getComputedStyle(document.documentElement).backgroundColor,
    foreground: getComputedStyle(document.documentElement).color,
  }));
  const mailColors = await page
    .frameLocator("iframe")
    .locator("body")
    .evaluate((body) => ({
      background: getComputedStyle(body).backgroundColor,
      foreground: getComputedStyle(body).color,
      overrides: body.querySelectorAll("[bgcolor],[color]").length,
    }));
  assert.equal(mailColors.background, "rgb(253, 246, 227)");
  assert.equal(mailColors.foreground, "rgb(18, 52, 86)");
  assert.ok(mailColors.overrides > 0);
  const cardStyles = await page
    .frameLocator("iframe")
    .locator(".mail-card")
    .evaluate((el) => ({
      padding: getComputedStyle(el).padding,
      fontSize: getComputedStyle(el).fontSize,
      fontWeight: getComputedStyle(el).fontWeight,
      display: getComputedStyle(el).display,
      gap: getComputedStyle(el).gap,
      position: getComputedStyle(el).position,
      fontFamily: getComputedStyle(el).fontFamily,
      shadow: getComputedStyle(el).boxShadow !== "none",
    }));
  assert.deepEqual(cardStyles, {
    padding: "24px",
    fontSize: "18px",
    fontWeight: "700",
    display: "grid",
    gap: "12px",
    position: "relative",
    fontFamily: "霞鹜臻楷",
    shadow: true,
  });
  await page.setViewportSize({ width: 2000, height: 1000 });
  const centered = await page.locator("iframe.mail-frame").evaluate((el) => {
    const r = el.getBoundingClientRect();
    const parent = el.parentElement.getBoundingClientRect();
    return {
      width: r.width,
      available: el.parentElement.clientWidth,
      maxWidth: getComputedStyle(el).maxWidth,
      offset: Math.abs(r.left + r.right - (parent.left + parent.right)),
    };
  });
  assert.ok(centered.width > 600);
  assert.ok(Math.abs(centered.width - centered.available) <= 1);
  assert.equal(centered.maxWidth, "none");
  async function assertEmailBoxCentered(selector, width) {
    const box = await page
      .frameLocator("iframe")
      .locator(selector)
      .evaluate((el) => {
        const rect = el.getBoundingClientRect();
        return {
          width: rect.width,
          offset: Math.abs(rect.left + rect.right - innerWidth),
        };
      });
    assert.ok(
      Math.abs(box.width - width) <= 1,
      `${selector} does not follow email width`,
    );
    assert.ok(box.offset <= 2, `${selector} is not centered`);
  }
  await assertEmailBoxCentered(".mail-card", 820);
  const initialRootStyle = await page
    .frameLocator("iframe")
    .locator("html")
    .getAttribute("style");
  const initialBodyStyle = await page
    .frameLocator("iframe")
    .locator("body")
    .getAttribute("style");
  await page
    .frameLocator("iframe")
    .locator("html")
    .evaluate((el) => (el.style.maxWidth = "1100px"));
  await page
    .frameLocator("iframe")
    .locator("body")
    .evaluate((el) => {
      el.style.maxWidth = "940px";
      el.style.width = "100%";
    });
  await assertEmailBoxCentered("html", 1100);
  await assertEmailBoxCentered("body", 940);
  await assertEmailBoxCentered(".mail-card", 820);
  const mailTable = page.frameLocator("iframe").locator("table");
  const initialTableStyle = await mailTable.getAttribute("style");
  await mailTable.evaluate((el) => {
    el.setAttribute("width", "760");
    el.style.maxWidth = "760px";
  });
  await assertEmailBoxCentered("table", 760);
  await mailTable.evaluate((el, style) => {
    el.removeAttribute("width");
    if (style === null) el.removeAttribute("style");
    else el.setAttribute("style", style);
  }, initialTableStyle);
  await page
    .frameLocator("iframe")
    .locator("body")
    .evaluate(
      (el, style) => el.setAttribute("style", style || ""),
      initialBodyStyle,
    );
  await page
    .frameLocator("iframe")
    .locator("html")
    .evaluate(
      (el, style) => el.setAttribute("style", style || ""),
      initialRootStyle,
    );
  assert.ok(
    !(await page.locator("iframe.mail-frame").getAttribute("sandbox")).includes(
      "allow-scripts",
    ),
  );
  assert.ok(centered.offset <= 2);
  await page.waitForFunction(() => {
    const frame = document.querySelector("iframe.mail-frame");
    const root = frame?.contentDocument?.documentElement;
    return (
      root &&
      root.clientHeight >= 1400 &&
      root.scrollHeight <= root.clientHeight
    );
  });
  // A late image/content resize must expand the frame rather than clip text.
  await page
    .frameLocator("iframe")
    .getByText("长邮件末尾测试", { exact: true })
    .evaluate((el) => (el.style.height = "1800px"));
  await page.waitForFunction(() => {
    const frame = document.querySelector("iframe.mail-frame");
    const root = frame?.contentDocument?.documentElement;
    return (
      root &&
      root.clientHeight >= 1800 &&
      root.scrollHeight <= root.clientHeight
    );
  });

  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "查看纯文本", exact: true }).click();
  assert.equal(await page.locator("pre.mail-text").textContent(), plainBody);
  const plainStyles = await page.locator("pre.mail-text").evaluate((el) => ({
    whiteSpace: getComputedStyle(el).whiteSpace,
    marginTop: getComputedStyle(el).marginTop,
    marginBottom: getComputedStyle(el).marginBottom,
    tabSize: getComputedStyle(el).tabSize,
    background: getComputedStyle(el).backgroundColor,
    fits: el.scrollWidth <= el.clientWidth,
    maxWidth: getComputedStyle(el).maxWidth,
    fillsReader:
      Math.abs(
        el.getBoundingClientRect().width - el.parentElement.clientWidth,
      ) <= 1,
  }));
  assert.equal(plainStyles.whiteSpace, "pre-wrap");
  assert.equal(plainStyles.marginTop, "0px");
  assert.equal(plainStyles.marginBottom, "0px");
  assert.equal(plainStyles.tabSize, "4");
  assert.equal(plainStyles.background, platformColors.background);
  assert.ok(plainStyles.fits);
  assert.equal(plainStyles.maxWidth, "none");
  assert.ok(plainStyles.fillsReader);
  assert.equal(await page.locator("pre.mail-text script").count(), 0);
  await page.getByRole("button", { name: "查看 HTML", exact: true }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  assert.equal(remoteRequests, 0);
  assert.equal(await page.evaluate(() => window.hacked), undefined);
  await page.screenshot({ path: "/tmp/nekoport-desktop.png" });
  await page.getByRole("button", { name: "本封允许图片" }).click();
  await page.waitForTimeout(200);
  assert.equal(
    remoteRequests,
    1,
    "single-message remote-image permission failed",
  );
  const archiveButton = page
    .locator(".reader-toolbar")
    .getByRole("button", { name: "归档邮件", exact: true });
  assert.equal(await archiveButton.isEnabled(), true);
  await archiveButton.click();
  await page
    .getByRole("heading", { name: "留一点空间，读一封邮件", exact: true })
    .waitFor();
  assert.deepEqual(
    mailOperations.filter((o) => o.kind === "move"),
    [{ ids: ["m"], kind: "move", value: false, target: "archive" }],
  );
  msg.mailboxId = "archive";
  await page.getByRole("button", { name: /项目团队.*项目进展/ }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  assert.equal(await archiveButton.isDisabled(), true);
  assert.equal(await archiveButton.getAttribute("title"), "邮件已在归档文件夹");
  msg.mailboxId = "inbox";
  await page
    .locator(".main-nav")
    .getByRole("button", { name: /所有收件箱/ })
    .click();
  await page.getByRole("button", { name: /项目团队.*项目进展/ }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  missingArchive = true;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.waitForFunction(
    () => document.querySelector('[aria-label="归档邮件"]')?.disabled === true,
  );
  assert.ok(
    (await archiveButton.getAttribute("title")).includes("未找到归档文件夹"),
  );
  missingArchive = false;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.waitForFunction(
    () => document.querySelector('[aria-label="归档邮件"]')?.disabled === false,
  );
  // Batch selection must follow acknowledged operations and visible rows.
  const second = { ...msg, id: "m2", subject: "批量测试第二封" };
  const third = { ...msg, id: "m3", subject: "批量测试第三封" };
  const firstCheck = page.getByRole("checkbox", {
    name: `选择 ${msg.subject}`,
    exact: true,
  });
  const secondCheck = page.getByRole("checkbox", {
    name: `选择 ${second.subject}`,
    exact: true,
  });
  const thirdCheck = page.getByRole("checkbox", {
    name: `选择 ${third.subject}`,
    exact: true,
  });
  listMessages = [msg, second, third];
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await thirdCheck.waitFor();
  await firstCheck.check();
  await secondCheck.check();
  await page.getByText("已选 2 封", { exact: true }).waitFor();
  rejectOperation = true;
  await page.getByRole("button", { name: "批量标为已读", exact: true }).click();
  await page.getByText("模拟批量操作失败", { exact: true }).waitFor();
  assert.equal(await firstCheck.isChecked(), true);
  assert.equal(await secondCheck.isChecked(), true);
  rejectOperation = false;
  let releaseOperation;
  holdOperation = new Promise((resolve) => {
    releaseOperation = resolve;
  });
  const request = page.waitForRequest(
    (req) =>
      req.url().endsWith("/message-operations") && req.method() === "POST",
  );
  await page.getByRole("button", { name: "批量标为已读", exact: true }).click();
  await request;
  await thirdCheck.check();
  releaseOperation();
  holdOperation = null;
  await page.getByText("已选 1 封", { exact: true }).waitFor();
  assert.equal(await firstCheck.isChecked(), false);
  assert.equal(await secondCheck.isChecked(), false);
  assert.equal(await thirdCheck.isChecked(), true);
  await page.getByRole("button", { name: "取消多选", exact: true }).click();
  await firstCheck.check();
  await secondCheck.check();
  removeOperationTargets = true;
  await page.getByRole("button", { name: "批量移动", exact: true }).click();
  const moveDialog = page.getByRole("dialog", {
    name: "移动到文件夹",
    exact: true,
  });
  await moveDialog
    .getByRole("button", { name: "目标文件夹", exact: true })
    .click();
  await page.getByRole("option", { name: "项目 2", exact: true }).click();
  await moveDialog.getByRole("button", { name: "移动", exact: true }).click();
  await firstCheck.waitFor({ state: "hidden" });
  assert.equal(await page.locator(".message-row").count(), 1);
  assert.equal(await page.locator(".bulk-bar").count(), 0);
  assert.deepEqual(mailOperations.at(-1), {
    ids: ["m", "m2"],
    kind: "move",
    value: false,
    target: "custom2",
  });
  // A server refresh may remove a selected message independently of an action.
  listMessages = [msg, second, third];
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await secondCheck.waitFor();
  await secondCheck.check();
  await thirdCheck.check();
  listMessages = [msg, third];
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await secondCheck.waitFor({ state: "hidden" });
  await page.getByText("已选 1 封", { exact: true }).waitFor();
  assert.equal(await thirdCheck.isChecked(), true);
  failedMessages = 1;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.getByText(/邮件列表暂时无法更新/).waitFor();
  assert.equal(await thirdCheck.isChecked(), true);
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.getByText(/邮件列表暂时无法更新/).waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "批量删除", exact: true }).click();
  await page
    .getByRole("dialog", { name: "移到已删除", exact: true })
    .getByRole("button", { name: "确认", exact: true })
    .click();
  await thirdCheck.waitFor({ state: "hidden" });
  assert.equal(await page.locator(".bulk-bar").count(), 0);
  assert.deepEqual(mailOperations.at(-1), {
    ids: ["m3"],
    kind: "trash",
    value: false,
    target: "",
  });
  listMessages = null;
  removeOperationTargets = false;
  await page.evaluate(() => window.dispatchEvent(new Event("online")));
  await page.getByRole("button", { name: /项目团队.*项目进展/ }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();

  // Exercise both reply and new-compose mounts. Run this script with the LAN
  // URL too: localhost is a secure context and masks randomUUID failures.
  await page.getByRole("button", { name: "回复", exact: true }).first().click();
  await page.getByRole("dialog", { name: "写邮件" }).waitFor();
  assert.ok(
    (await page.getByLabel("主题", { exact: true }).inputValue()).startsWith(
      "Re:",
    ),
  );
  await page.getByRole("button", { name: "保存并关闭", exact: true }).click();
  await page
    .getByRole("dialog", { name: "写邮件" })
    .waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "写邮件", exact: true }).click();
  const composeFont = await page.locator(".compose-body").evaluate((el) => ({
    actual: getComputedStyle(el).fontFamily,
    expected: getComputedStyle(document.documentElement).fontFamily,
  }));
  assert.equal(composeFont.actual, composeFont.expected);

  await page
    .getByLabel("收件人", { exact: true })
    .fill("recipient@example.test");
  await page.getByLabel("主题", { exact: true }).fill("浏览器测试邮件");
  await page.getByRole("textbox", { name: "纯文本正文" }).fill("第一版");
  await page.waitForTimeout(1000);
  await page
    .getByRole("textbox", { name: "纯文本正文" })
    .fill("第二版：保存应保留这份内容。");
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await page
    .getByRole("dialog", { name: "写邮件" })
    .waitFor({ state: "hidden" });
  assert.ok(savedCount >= 1);
  assert.equal(body, "第二版：保存应保留这份内容。");
  assert.equal(sentCount, 1);
  await page.setViewportSize({ width: 390, height: 844 });
  // Establish the mobile navigation state independently of the compose flow.
  await page.locator(".brand").click();
  await page.getByRole("heading", { name: "邮件概览", exact: true }).waitFor();
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.waitForFunction(
    () => document.querySelector(".sidebar").getBoundingClientRect().right <= 1,
  );
  await page.screenshot({ path: "/tmp/nekoport-overview-mobile.png" });
  await page.getByRole("button", { name: "查看收件箱", exact: true }).click();
  await page.locator(".message-panel").waitFor({ state: "visible" });
  await page.getByRole("button", { name: /项目团队.*项目进展/ }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  await page.getByRole("button", { name: "返回邮件列表" }).click();
  await page.locator(".message-panel").waitFor({ state: "visible" });
  assert.equal(await page.locator(".reader-panel").isVisible(), false);
  assert.ok(await page.locator(".message-panel").isVisible());
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.getByRole("button", { name: /项目团队.*项目进展/ }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.getByRole("button", { name: "查看纯文本", exact: true }).click();
  assert.ok(
    await page
      .locator("pre.mail-text")
      .evaluate((el) => el.scrollWidth <= el.clientWidth),
  );
  assert.equal(await page.locator("pre.mail-text").textContent(), plainBody);
  await page.screenshot({ path: "/tmp/nekoport-plaintext-mobile.png" });
  await page.getByRole("button", { name: "查看 HTML", exact: true }).click();
  await page.frameLocator("iframe").getByText("你好，这是一封邮件。").waitFor();
  await page.waitForFunction(() => {
    const f = document.querySelector("iframe.mail-frame");
    const root = f?.contentDocument?.documentElement;
    return root && root.scrollHeight <= root.clientHeight;
  });
  assert.ok(
    await page
      .locator(".reader-toolbar")
      .getByRole("button", { name: "归档邮件", exact: true })
      .isVisible(),
  );
  assert.ok(
    await page
      .locator(".reader-toolbar")
      .evaluate((el) => el.scrollWidth <= el.clientWidth),
  );
  await checkMoveMenu();
  await page.screenshot({ path: "/tmp/nekoport-mobile.png" });
  await page.getByRole("button", { name: "展开邮箱导航" }).click();
  await page.getByRole("button", { name: "添加邮箱", exact: true }).click();
  await page.getByRole("dialog", { name: "添加邮箱" }).waitFor();
  assert.equal(await page.getByLabel("服务器地址").first().inputValue(), "");
  await page.setViewportSize({ width: 1440, height: 900 });
  const accountModal = page.getByRole("dialog", {
    name: "添加邮箱",
    exact: true,
  });
  assert.equal(
    await accountModal.getByLabel("登录用户名", { exact: true }).count(),
    1,
  );
  assert.equal(
    await accountModal.getByLabel("密码 / 应用密码", { exact: true }).count(),
    1,
  );
  assert.equal(
    await accountModal
      .getByRole("checkbox", { name: /相同的登录信息/ })
      .count(),
    0,
  );
  const servers = accountModal.locator(".server-section");
  for (const label of ["服务器地址", "端口"]) {
    const left = await servers
      .nth(0)
      .getByLabel(label, { exact: true })
      .boundingBox();
    const right = await servers
      .nth(1)
      .getByLabel(label, { exact: true })
      .boundingBox();
    assert.ok(Math.abs(left.y - right.y) <= 1, `${label} not aligned`);
  }
  await page.screenshot({ path: "/tmp/nekoport-account.png" });
  await page.keyboard.press("Escape");
  await page.locator(".brand").click();
  await page.getByRole("heading", { name: "邮件概览", exact: true }).waitFor();
  await page.getByRole("textbox", { name: "搜索邮件" }).fill("项目");
  await page
    .getByRole("heading", { name: "所有收件箱", exact: true })
    .waitFor();
  await page.getByRole("button", { name: /项目团队.*项目进展/ }).waitFor();
  await page.getByRole("textbox", { name: "搜索邮件" }).fill("");
  await page.getByRole("button", { name: "打开设置", exact: true }).click();
  await page
    .getByRole("heading", { name: "设置与同步", exact: true })
    .waitFor();
  assert.equal(
    await page.locator(".account-card .account-sync-panel").count(),
    0,
  );
  const syncButton = page.getByRole("button", {
    name: "同步设置",
    exact: true,
  });
  assert.equal(
    await page
      .locator(".account-card > footer")
      .filter({ has: syncButton })
      .count(),
    1,
  );
  await syncButton.click();
  await page.getByRole("dialog", { name: "同步设置", exact: true }).waitFor();
  assert.equal(await page.locator("select").count(), 0);
  const scopeSelect = page.getByRole("button", {
    name: "同步范围",
    exact: true,
  });
  await scopeSelect.click();
  await page.getByRole("option", { name: "全部历史", exact: true }).click();
  assert.equal(
    await page.getByLabel("最近同步天数", { exact: true }).isDisabled(),
    true,
  );
  await scopeSelect.press("ArrowDown");
  await page.getByRole("listbox", { name: "同步范围", exact: true }).waitFor();
  await page.keyboard.press("Escape");
  assert.ok(
    await page
      .getByRole("dialog", { name: "同步设置", exact: true })
      .isVisible(),
  );
  assert.equal(
    await page.getByRole("listbox", { name: "同步范围", exact: true }).count(),
    0,
  );
  await scopeSelect.click();
  await page.getByRole("option", { name: "最近指定天数", exact: true }).click();
  const historyPanel = page.locator(".sync-history");
  assert.equal(await historyPanel.evaluate((el) => el.open), false);
  await historyPanel.locator("summary").click();
  await page.getByText("暂无完整对齐记录。", { exact: true }).waitFor();
  await historyPanel.locator("summary").click();
  await page.screenshot({ path: "/tmp/nekoport-sync-modal-desktop.png" });
  await page.getByRole("button", { name: "同步全部历史", exact: true }).click();
  await page
    .locator(".sync-history > summary")
    .filter({ hasText: "1 条记录" })
    .waitFor();
  assert.equal(await historyPanel.evaluate((el) => el.open), false);
  await historyPanel.locator("summary").click();
  await page.getByRole("button", { name: /全历史同步 · 等待执行/ }).waitFor();
  failedTaskReads = 1;
  await page.locator(".task-refresh-warning").waitFor();
  assert.ok(
    await page
      .getByRole("button", { name: /全历史同步 · 等待执行/ })
      .isVisible(),
  );
  await page
    .locator(".task-refresh-warning")
    .getByRole("button", { name: "重试读取", exact: true })
    .click();
  await page.locator(".task-refresh-warning").waitFor({ state: "hidden" });

  await page.getByRole("button", { name: "取消", exact: true }).click();
  await page.getByRole("button", { name: /全历史同步 · 已取消/ }).waitFor();
  await page.getByRole("button", { name: "继续 / 重试", exact: true }).click();
  await page.getByRole("button", { name: /全历史同步 · 等待执行/ }).waitFor();
  await page.getByRole("checkbox", { name: "定时对齐服务器邮件" }).uncheck();
  await page.getByRole("button", { name: "保存同步设置", exact: true }).click();
  await page.getByText("定时对齐已关闭", { exact: true }).waitFor();
  await page
    .getByRole("dialog", { name: "同步设置", exact: true })
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  await page
    .getByRole("dialog", { name: "同步设置", exact: true })
    .waitFor({ state: "hidden" });
  assert.equal(await page.locator(".account-sync-panel").count(), 0);
  assert.equal(
    await syncButton.evaluate((el) => el === document.activeElement),
    true,
  );
  for (let n = 0; n < 5; n++) {
    const stats = {
      compared: 10 + n,
      added: 2,
      updated: 0,
      unchanged: 8 + n,
      deleted: 0,
    };
    syncTasks.push({
      id: `quick-log-${n}`,
      finishedAt: new Date().toISOString(),
      accountId: "a",
      kind: "quick",
      state: "succeeded",
      trigger: "timer",
      createdAt: new Date().toISOString(),
      logVersion: 1,
      stats,
      folders: [],
      logs: [
        {
          at: new Date().toISOString(),
          action: "compare",
          folder: "INBOX",
          message: "增量新增邮件，未执行删除检查",
          firstUid: 11,
          lastUid: 12,
          stats,
        },
      ],
    });
  }
  await page.setViewportSize({ width: 390, height: 844 });
  await syncButton.click();
  const syncModal = page.getByRole("dialog", { name: "同步设置", exact: true });
  await syncModal.getByText("定时对齐已关闭", { exact: true }).waitFor();
  assert.equal(
    await syncModal
      .getByRole("button", { name: "同步范围", exact: true })
      .innerText(),
    "全部历史",
  );
  assert.equal(
    await syncModal.locator(".sync-history").evaluate((el) => el.open),
    false,
  );
  await syncModal.locator(".sync-history > summary").click();
  await syncModal
    .getByRole("button", { name: /全历史同步 · 等待执行/ })
    .waitFor();
  await syncModal.getByRole("tab", { name: /快速同步/ }).click();
  assert.equal(
    await syncModal.getByRole("button", { name: /快速同步 · 已完成/ }).count(),
    5,
  );
  assert.equal(
    await syncModal
      .getByRole("button", { name: /全历史同步 · 等待执行/ })
      .count(),
    0,
  );
  await syncModal
    .getByRole("button", { name: /快速同步 · 已完成/ })
    .first()
    .click();
  await syncModal
    .getByText("增量新增邮件，未执行删除检查", { exact: false })
    .waitFor();
  await syncModal.getByText("UID 11–12", { exact: true }).waitFor();
  async function checkLogMetadataAlignment() {
    const rows = await syncModal
      .locator(".sync-log-times > div")
      .evaluateAll((elements) =>
        elements.map((row) => {
          const label = row.querySelector("dt"),
            value = row.querySelector("dd");
          const a = label.getBoundingClientRect(),
            b = value.getBoundingClientRect();
          return {
            labelSize: getComputedStyle(label).fontSize,
            valueSize: getComputedStyle(value).fontSize,
            labelLine: getComputedStyle(label).lineHeight,
            valueLine: getComputedStyle(value).lineHeight,
            labelLeft: a.left,
            valueLeft: b.left,
            aligned: Math.abs(a.top - b.top) <= 1,
            fits: value.scrollWidth <= value.clientWidth,
          };
        }),
      );
    assert.equal(rows.length, 3);
    for (const row of rows) {
      assert.equal(row.labelSize, row.valueSize);
      assert.equal(row.labelLine, row.valueLine);
      assert.equal(row.valueSize, "11px");
      assert.ok(row.aligned && row.fits);
      assert.ok(Math.abs(row.labelLeft - rows[0].labelLeft) <= 1);
      assert.ok(Math.abs(row.valueLeft - rows[0].valueLeft) <= 1);
    }
  }
  await checkLogMetadataAlignment();
  await syncModal
    .locator(".sync-log-times")
    .screenshot({ path: "/tmp/nekoport-sync-log-times-mobile.png" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await checkLogMetadataAlignment();
  await syncModal
    .locator(".sync-log-times")
    .screenshot({ path: "/tmp/nekoport-sync-log-times-desktop.png" });
  await page.setViewportSize({ width: 390, height: 844 });

  assert.ok(
    (await syncModal.locator(".sync-task-stats").first().innerText()).includes(
      "新增 2",
    ),
  );
  await syncModal.getByRole("tab", { name: /完整对齐/ }).click();
  await syncModal
    .getByRole("button", { name: /全历史同步 · 等待执行/ })
    .waitFor();
  await syncModal.locator(".sync-history > summary").click();
  const modalBounds = await syncModal.boundingBox();
  assert.ok(
    modalBounds.x >= 0 &&
      modalBounds.x + modalBounds.width <= 390 &&
      modalBounds.y >= 0 &&
      modalBounds.y + modalBounds.height <= 844,
  );
  await page.screenshot({ path: "/tmp/nekoport-sync-modal-mobile.png" });
  await page.keyboard.press("Escape");
  await syncModal.waitFor({ state: "hidden" });
  await page.setViewportSize({ width: 1440, height: 900 });
  const security = page.locator(".account-security");
  const passwordSwitch = security.getByRole("switch", {
    name: "修改密码",
    exact: true,
  });
  assert.equal(await passwordSwitch.isChecked(), false);
  await passwordSwitch.focus();
  await passwordSwitch.press("Space");
  assert.equal(await passwordSwitch.isChecked(), true);
  await security.getByLabel("新密码", { exact: true }).fill("mock-password");
  await security
    .getByLabel("确认新密码", { exact: true })
    .fill("mock-password");
  await security.screenshot({
    path: "/tmp/nekoport-account-security-switch.png",
  });
  await passwordSwitch.click();
  assert.equal(await security.getByLabel("新密码", { exact: true }).count(), 0);
  await passwordSwitch.click();
  assert.equal(
    await security.getByLabel("新密码", { exact: true }).inputValue(),
    "",
  );
  assert.equal(
    await security.getByLabel("确认新密码", { exact: true }).inputValue(),
    "",
  );
  await passwordSwitch.click();

  await security.getByLabel("登录用户名", { exact: true }).fill("new-admin");
  await security.getByLabel("当前密码", { exact: true }).fill("wrong-password");
  await security
    .getByRole("button", { name: "保存并重新登录", exact: true })
    .click();
  await security
    .getByRole("alert")
    .filter({ hasText: "当前密码不正确" })
    .waitFor();
  assert.equal(
    await security.getByLabel("登录用户名", { exact: true }).inputValue(),
    "new-admin",
  );
  await security.getByLabel("当前密码", { exact: true }).fill("test-password");
  await security
    .getByRole("button", { name: "保存并重新登录", exact: true })
    .click();
  await page.getByRole("heading", { name: "欢迎回来", exact: true }).waitFor();
  await page
    .getByText("账户已更新，请使用新的账号信息重新登录。", { exact: true })
    .waitFor();
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    const login = await page.locator(".login-layout").boundingBox();
    assert.equal(login.x, 0);
    assert.equal(login.y, 0);
    assert.equal(login.width, viewport.width);
    assert.equal(await page.locator(".app-shell").count(), 0);
  }
  authenticated = true;
  emptyOverview = true;
  await page.reload();
  await page.getByRole("heading", { name: "邮件概览", exact: true }).waitFor();
  await page
    .getByRole("button", { name: "添加第一个邮箱", exact: true })
    .waitFor();
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({ path: "/tmp/nekoport-overview-empty-mobile.png" });
  await page
    .getByRole("button", { name: "添加第一个邮箱", exact: true })
    .click();
  await page.getByRole("dialog", { name: "添加邮箱" }).waitFor();
  await page.keyboard.press("Escape");
  assert.deepEqual(errors, []);
  console.log(
    "浏览器验证通过：批量操作后选择计数、失败保留选择、请求期间新增选择及刷新移除失效选择、移动菜单越过弹窗裁切、向上展开及手机完整选择、归档按钮与无目标/已归档保护、默认文件夹顺序与自定义文件夹自然排序、网络恢复自动清理刷新提示且保留操作错误、发件人名称与历史发件箱地址显示、修改密码滑动开关、自定义下拉框与对齐历史折叠、同步设置弹窗桌面/手机与关闭重开、账户安全、全历史任务取消/继续、定时设置及共享登录信息/服务器表单对齐、概览首屏、空邮箱引导与搜索导航、宽屏/手机布局、纯文本缩进与长行折行、邮件原始配色、邮件自身宽度与外层布局居中、自适应高度、HTML 隔离、图片授权及草稿发送。",
  );
  await context.close();
} catch (error) {
  const page = browser.contexts()[0]?.pages()[0];
  if (page) {
    console.error("失败时页面状态：", await page.locator("body").innerText());
    console.error(
      "布局：",
      await page.evaluate(() => ({
        url: location.href,
        width: innerWidth,
        workspace: document.querySelector(".workspace")?.className,
        reader: document
          .querySelector(".reader-panel")
          ?.getBoundingClientRect()
          .toJSON(),
        list: document
          .querySelector(".message-panel")
          ?.getBoundingClientRect()
          .toJSON(),
      })),
    );
    await page.screenshot({ path: "/tmp/nekoport-failure.png" });
  }
  throw error;
} finally {
  await browser.close();
  await new Promise((ok) => server.close(ok));
  await sourceServer?.close();
}

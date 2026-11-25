#!/usr/bin/env node
import http from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const args = parseArgs(process.argv.slice(2));
const bridgeScriptPath = fileURLToPath(import.meta.url);
const browserRoot = args["browser-root"] ? path.resolve(String(args["browser-root"])) : "";
if (browserRoot) process.env.PLAYWRIGHT_BROWSERS_PATH = browserRoot;
const { chromium } = loadPlaywright(args["node-module-root"]);
const stateDir = path.resolve(args["state-dir"] || ".browser-control-bridge");
const requestedPort = Number(args.port || 0);

let config = {
  browserProfileDir: path.join(stateDir, "browser-profile"),
  browserHeadless: true,
};

let context;
let page;
let mode = "closed";
let operationQueue = Promise.resolve();
let logEntries = [];
let requestLogEntries = new WeakMap();
let nextLogSequence = 0;
let nextNetworkRequestId = 0;
const trackedPages = new WeakSet();
const maxCapturedLogEntries = 2000;

const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url || "/", "http://127.0.0.1");
    if (req.method === "GET" && url.pathname === "/health") return json(res, 200, { ok: true });
    if (req.method === "POST" && url.pathname === "/config") return json(res, 200, await updateConfig(await readBody(req)));
    if (req.method === "GET" && url.pathname === "/status") return json(res, 200, await status());
    if (req.method === "POST" && url.pathname.startsWith("/tool/")) {
      const toolName = decodeURIComponent(url.pathname.slice("/tool/".length));
      return json(res, 200, await callQueued(toolName, await readBody(req)));
    }
    json(res, 404, { error: "not found" });
  } catch (error) {
    json(res, 400, { error: String(error?.message || error) });
  }
});

await fs.mkdir(stateDir, { recursive: true });
server.listen(requestedPort, "127.0.0.1", () => {
  const address = server.address();
  process.stdout.write(JSON.stringify({ event: "ready", url: `http://127.0.0.1:${address.port}` }) + "\n");
});

process.on("SIGINT", async () => {
  await closeBrowser();
  process.exit(0);
});

async function updateConfig(next) {
  const oldHeadless = config.browserHeadless;
  const oldProfile = config.browserProfileDir;
  config = normalizeConfig({ ...config, ...(next || {}) });
  await fs.mkdir(config.browserProfileDir, { recursive: true });
  if (mode === "browser" && (oldHeadless !== config.browserHeadless || oldProfile !== config.browserProfileDir)) {
    await closeBrowser();
  }
  return { ok: true, config: publicConfig() };
}

async function status() {
  const current = page;
  return withMeta({
    running: Boolean(context && current),
    mode,
    hidden: config.browserHeadless,
    url: current?.url() || null,
    title: current ? await safeTitle(current) : null,
  }, "status");
}

async function callQueued(name, input) {
  const run = async () => callTool(name, input);
  operationQueue = operationQueue.then(run, run);
  return operationQueue;
}

async function callTool(name, input) {
  switch (name) {
    case "cua_browser_status":
      return status();
    case "cua_browser_navigate":
      return browserNavigate(input);
    case "cua_browser_snapshot":
      return browserSnapshot(input);
    case "cua_browser_click":
      return browserClick(input);
    case "cua_browser_type":
      return browserType(input);
    case "cua_browser_press":
      return browserPress(input);
    case "cua_browser_wait":
      return browserWait(input);
    case "cua_browser_back":
      return browserBack();
    case "cua_browser_logs":
      return browserLogs(input);
    case "cua_browser_clear_logs":
      return browserClearLogs(input);
    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}

async function browserNavigate(input) {
  const p = await ensurePage();
  const target = normalizeNavigateUrl(String(input?.url || ""));
  const waitUntil = ["load", "domcontentloaded", "networkidle"].includes(String(input?.waitUntil || ""))
    ? String(input.waitUntil)
    : "domcontentloaded";
  await p.goto(target, { waitUntil, timeout: 30000 });
  await settle(p);
  return withMeta(await pageSummary(p), "navigate");
}

async function browserSnapshot(input) {
  const p = await ensurePage();
  await settle(p);
  const maxElements = clampInt(input?.maxElements, 80, 1, 200);
  const maxTextChars = clampInt(input?.maxTextChars, 6000, 200, 30000);
  const snapshot = await p.evaluate(({ maxElements, maxTextChars }) => {
    const selectorFor = (el) => {
      const cssEscape = (value) => {
        if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(String(value));
        return String(value).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
      };
      if (el.id) return `#${cssEscape(el.id)}`;
      for (const attr of ["data-testid", "data-test", "data-cy"]) {
        const testId = el.getAttribute(attr);
        if (testId) return `[${attr}="${String(testId).replaceAll('"', '\\"')}"]`;
      }
      const name = el.getAttribute("name");
      if (name) return `${el.tagName.toLowerCase()}[name="${String(name).replaceAll('"', '\\"')}"]`;
      const aria = el.getAttribute("aria-label");
      if (aria) return `${el.tagName.toLowerCase()}[aria-label="${String(aria).replaceAll('"', '\\"')}"]`;
      const parts = [];
      let node = el;
      while (node && node.nodeType === Node.ELEMENT_NODE && parts.length < 4) {
        let part = node.tagName.toLowerCase();
        const parent = node.parentElement;
        if (parent) {
          const same = Array.from(parent.children).filter((child) => child.tagName === node.tagName);
          if (same.length > 1) part += `:nth-of-type(${same.indexOf(node) + 1})`;
        }
        parts.unshift(part);
        node = parent;
      }
      return parts.join(" > ");
    };
    const textFor = (el) => {
      const aria = el.getAttribute("aria-label");
      const placeholder = el.getAttribute("placeholder");
      const value = el.getAttribute("value");
      const text = (el.innerText || el.textContent || "").replace(/\s+/g, " ").trim();
      return (text || aria || placeholder || value || "").slice(0, 220);
    };
    const candidates = Array.from(document.querySelectorAll(
      'a, button, input, textarea, select, [role="button"], [role="link"], [contenteditable="true"], [tabindex]:not([tabindex="-1"])'
    )).filter((el) => {
      const rect = el.getBoundingClientRect();
      const style = window.getComputedStyle(el);
      return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
    });
    const elements = candidates.slice(0, maxElements).map((el, index) => {
      const rect = el.getBoundingClientRect();
      return {
        index: index + 1,
        tag: el.tagName.toLowerCase(),
        role: el.getAttribute("role") || null,
        text: textFor(el),
        label: el.getAttribute("aria-label") || null,
        placeholder: el.getAttribute("placeholder") || null,
        href: el.href || null,
        disabled: Boolean(el.disabled || el.getAttribute("aria-disabled") === "true"),
        selector: selectorFor(el),
        bounds: {
          x: Math.round(rect.x),
          y: Math.round(rect.y),
          width: Math.round(rect.width),
          height: Math.round(rect.height),
        },
      };
    });
    const fullText = (document.body?.innerText || "").replace(/\n{3,}/g, "\n\n").trim();
    return {
      url: window.location.href,
      title: document.title || "",
      text: fullText.slice(0, maxTextChars),
      text_truncated: fullText.length > maxTextChars,
      elements,
      element_count_total: candidates.length,
    };
  }, { maxElements, maxTextChars });
  return withMeta(snapshot, "snapshot");
}

async function browserClick(input) {
  const p = await ensurePage();
  const locator = await resolveLocator(p, input);
  await locator.click({ timeout: 15000 });
  await settle(p);
  return withMeta(await pageSummary(p), "click");
}

async function browserType(input) {
  const p = await ensurePage();
  const value = String(input?.value ?? "");
  if (!value) throw new Error("value is required");
  const locator = await resolveLocator(p, input);
  const clear = input?.clear !== false;
  if (clear) {
    await locator.fill(value, { timeout: 15000 });
  } else {
    await locator.click({ timeout: 15000 });
    await locator.type(value, { timeout: 15000 });
  }
  await settle(p);
  return withMeta(await pageSummary(p), "type");
}

async function browserPress(input) {
  const p = await ensurePage();
  const key = String(input?.key || "").trim();
  if (!key) throw new Error("key is required");
  await p.keyboard.press(key, { delay: 20 });
  await settle(p);
  return withMeta(await pageSummary(p), "press");
}

async function browserWait(input) {
  const p = await ensurePage();
  const milliseconds = clampInt(input?.milliseconds, 1000, 100, 30000);
  await p.waitForTimeout(milliseconds);
  await settle(p);
  return withMeta(await pageSummary(p), "wait");
}

async function browserBack() {
  const p = await ensurePage();
  await p.goBack({ waitUntil: "domcontentloaded", timeout: 15000 }).catch(() => null);
  await settle(p);
  return withMeta(await pageSummary(p), "back");
}

async function browserLogs(input) {
  const filter = normalizeLogFilter(input || {});
  const matching = logEntries.filter((entry) => logEntryMatches(entry, filter));
  const totalMatching = matching.length;
  let entries = matching;
  if (entries.length > filter.limit) entries = entries.slice(entries.length - filter.limit);
  if (filter.order === "desc") entries = [...entries].reverse();
  return withMeta({
    entries: entries.map(publicLogEntry),
    returned: entries.length,
    total_matching: totalMatching,
    total_captured: logEntries.length,
    high_watermark_sequence: nextLogSequence,
    filters: filter.public,
    summary: summarizeLogs(entries),
  }, "logs");
}

async function browserClearLogs(input) {
  const sources = normalizeLogSources(input?.types);
  const before = logEntries.length;
  if (sources.size === 3) {
    logEntries = [];
    requestLogEntries = new WeakMap();
  } else {
    logEntries = logEntries.filter((entry) => !sources.has(entry.source));
  }
  return withMeta({
    cleared: before - logEntries.length,
    remaining: logEntries.length,
    high_watermark_sequence: nextLogSequence,
  }, "clear_logs");
}

async function resolveLocator(p, input) {
  const selector = String(input?.selector || "").trim();
  if (selector) return p.locator(selector).first();
  const text = String(input?.text || "").trim();
  if (text) return p.getByText(text, { exact: false }).first();
  const index = Number(input?.index || 0);
  if (Number.isFinite(index) && index > 0) {
    return p.locator(
      'a, button, input, textarea, select, [role="button"], [role="link"], [contenteditable="true"], [tabindex]:not([tabindex="-1"])'
    ).nth(Math.floor(index) - 1);
  }
  throw new Error("selector, text, or 1-based index is required");
}

async function ensurePage() {
  if (context && page) return page;
  return launch();
}

async function launch() {
  await fs.mkdir(config.browserProfileDir, { recursive: true });
  context = await chromium.launchPersistentContext(config.browserProfileDir, {
    headless: config.browserHeadless,
    viewport: { width: 1440, height: 1000 },
    acceptDownloads: true,
    chromiumSandbox: true,
  });
  context.on("page", (nextPage) => {
    trackPage(nextPage);
    page = nextPage;
  });
  page = context.pages()[0] || await context.newPage();
  for (const existing of context.pages()) trackPage(existing);
  trackPage(page);
  mode = "browser";
  return page;
}

async function closeBrowser() {
  const c = context;
  context = undefined;
  page = undefined;
  mode = "closed";
  if (c) await c.close().catch(() => null);
}

async function settle(p) {
  await p.waitForLoadState("domcontentloaded", { timeout: 5000 }).catch(() => null);
  await p.waitForLoadState("networkidle", { timeout: 1500 }).catch(() => null);
}

function trackPage(p) {
  if (!p || trackedPages.has(p)) return;
  trackedPages.add(p);
  p.on("console", (message) => captureConsoleLog(p, message));
  p.on("pageerror", (error) => capturePageError(p, error));
  p.on("request", (request) => captureNetworkRequest(p, request));
  p.on("response", (response) => captureNetworkResponse(response));
  p.on("requestfailed", (request) => captureNetworkFailure(request));
  p.on("requestfinished", (request) => captureNetworkFinished(request));
}

function captureConsoleLog(p, message) {
  const location = message.location?.() || {};
  pushLogEntry({
    source: "console",
    level: String(message.type?.() || "log"),
    text: truncateLogText(message.text?.() || ""),
    url: safePageUrl(p),
    location: {
      url: location.url || null,
      line: Number.isFinite(location.lineNumber) ? location.lineNumber : null,
      column: Number.isFinite(location.columnNumber) ? location.columnNumber : null,
    },
  });
}

function capturePageError(p, error) {
  pushLogEntry({
    source: "pageerror",
    level: "error",
    text: truncateLogText(error?.message || String(error || "")),
    stack: truncateLogText(error?.stack || "", 8000),
    url: safePageUrl(p),
  });
}

function captureNetworkRequest(p, request) {
  const entry = {
    source: "network",
    request_id: ++nextNetworkRequestId,
    method: request.method(),
    url: request.url(),
    page_url: safePageUrl(p),
    resource_type: request.resourceType(),
    navigation_request: request.isNavigationRequest(),
    status: null,
    status_text: null,
    ok: null,
    failed: false,
    failure_text: null,
    response_content_type: null,
    duration_ms: null,
    finished_at: null,
    started_at_ms: Date.now(),
  };
  requestLogEntries.set(request, entry);
  pushLogEntry(entry);
  return entry;
}

function captureNetworkResponse(response) {
  const request = response.request();
  const entry = requestLogEntries.get(request);
  if (!entry) return;
  entry.status = response.status();
  entry.status_text = response.statusText();
  entry.ok = response.ok();
  const headers = response.headers();
  entry.response_content_type = firstHeader(headers, "content-type");
}

async function captureNetworkFinished(request) {
  const entry = requestLogEntries.get(request);
  if (!entry) return;
  entry.finished_at = new Date().toISOString();
  entry.duration_ms = Math.max(0, Date.now() - entry.started_at_ms);
  const response = await request.response().catch(() => null);
  if (response) captureNetworkResponse(response);
}

function captureNetworkFailure(request) {
  const entry = requestLogEntries.get(request);
  if (!entry) return;
  entry.failed = true;
  entry.ok = false;
  entry.failure_text = request.failure()?.errorText || "request failed";
  entry.finished_at = new Date().toISOString();
  entry.duration_ms = Math.max(0, Date.now() - entry.started_at_ms);
}

function pushLogEntry(entry) {
  entry.sequence = ++nextLogSequence;
  entry.timestamp = new Date().toISOString();
  logEntries.push(entry);
  if (logEntries.length > maxCapturedLogEntries) {
    logEntries.splice(0, logEntries.length - maxCapturedLogEntries);
  }
}

function publicLogEntry(entry) {
  const copy = { ...entry };
  delete copy.started_at_ms;
  return copy;
}

function normalizeLogFilter(input) {
  const urlRegex = compileOptionalRegex(input.urlRegex, "urlRegex");
  const textRegex = compileOptionalRegex(input.textRegex, "textRegex");
  const statusClasses = normalizeStringList(input.statusClass ?? input.statusClasses)
    .map((value) => value.toLowerCase())
    .filter((value) => /^[1-5]xx$/.test(value));
  const filter = {
    sources: normalizeLogSources(input.types),
    levels: new Set(normalizeStringList(input.levels).map((value) => value.toLowerCase())),
    methods: new Set(normalizeStringList(input.methods).map((value) => value.toUpperCase())),
    resourceTypes: new Set(normalizeStringList(input.resourceTypes).map((value) => value.toLowerCase())),
    statusCodes: new Set(normalizeNumberList(input.statusCodes)),
    statusClasses: new Set(statusClasses),
    statusMin: finiteNumberOrNull(input.statusMin),
    statusMax: finiteNumberOrNull(input.statusMax),
    urlContains: String(input.urlContains || "").trim().toLowerCase(),
    urlRegex,
    textContains: String(input.textContains || "").trim().toLowerCase(),
    textRegex,
    failedOnly: input.failedOnly === true,
    sinceSequence: Math.max(0, Math.floor(finiteNumberOrNull(input.sinceSequence) || 0)),
    sinceMsCutoff: sinceMsCutoff(input.sinceMs),
    limit: clampInt(input.limit, 100, 1, 500),
    order: String(input.order || "asc").toLowerCase() === "desc" ? "desc" : "asc",
  };
  filter.public = {
    types: [...filter.sources],
    levels: [...filter.levels],
    methods: [...filter.methods],
    resourceTypes: [...filter.resourceTypes],
    statusCodes: [...filter.statusCodes],
    statusClass: [...filter.statusClasses],
    statusMin: filter.statusMin,
    statusMax: filter.statusMax,
    urlContains: input.urlContains || "",
    urlRegex: input.urlRegex || "",
    textContains: input.textContains || "",
    textRegex: input.textRegex || "",
    failedOnly: filter.failedOnly,
    sinceSequence: filter.sinceSequence,
    sinceMs: finiteNumberOrNull(input.sinceMs),
    limit: filter.limit,
    order: filter.order,
  };
  return filter;
}

function logEntryMatches(entry, filter) {
  if (!filter.sources.has(entry.source)) return false;
  if (filter.sinceSequence > 0 && Number(entry.sequence || 0) <= filter.sinceSequence) return false;
  if (filter.sinceMsCutoff && Date.parse(entry.timestamp || "") < filter.sinceMsCutoff) return false;
  if (filter.levels.size > 0 && !filter.levels.has(String(entry.level || "").toLowerCase())) return false;
  if (filter.methods.size > 0 && !filter.methods.has(String(entry.method || "").toUpperCase())) return false;
  if (filter.resourceTypes.size > 0 && !filter.resourceTypes.has(String(entry.resource_type || "").toLowerCase())) return false;
  if (!statusMatches(entry, filter)) return false;
  if (filter.failedOnly && !isFailedLogEntry(entry)) return false;
  const urlText = logUrlText(entry).toLowerCase();
  if (filter.urlContains && !urlText.includes(filter.urlContains)) return false;
  if (filter.urlRegex && !filter.urlRegex.test(urlText)) return false;
  const searchText = logSearchText(entry).toLowerCase();
  if (filter.textContains && !searchText.includes(filter.textContains)) return false;
  if (filter.textRegex && !filter.textRegex.test(searchText)) return false;
  return true;
}

function statusMatches(entry, filter) {
  const hasStatusFilter = filter.statusCodes.size > 0
    || filter.statusClasses.size > 0
    || filter.statusMin !== null
    || filter.statusMax !== null;
  if (!hasStatusFilter) return true;
  const status = Number(entry.status);
  if (!Number.isFinite(status)) return false;
  if (filter.statusCodes.size > 0 && !filter.statusCodes.has(status)) return false;
  if (filter.statusClasses.size > 0 && !filter.statusClasses.has(`${Math.floor(status / 100)}xx`)) return false;
  if (filter.statusMin !== null && status < filter.statusMin) return false;
  if (filter.statusMax !== null && status > filter.statusMax) return false;
  return true;
}

function isFailedLogEntry(entry) {
  if (entry.source === "network") return entry.failed === true || Number(entry.status || 0) >= 400;
  return entry.source === "pageerror" || String(entry.level || "").toLowerCase() === "error";
}

function summarizeLogs(entries) {
  const summary = {
    by_source: {},
    console_levels: {},
    network: {
      by_method: {},
      by_resource_type: {},
      by_status_class: {},
      failed: 0,
    },
  };
  for (const entry of entries) {
    increment(summary.by_source, entry.source || "unknown");
    if (entry.source === "console" || entry.source === "pageerror") {
      increment(summary.console_levels, entry.level || "unknown");
    }
    if (entry.source === "network") {
      increment(summary.network.by_method, entry.method || "UNKNOWN");
      increment(summary.network.by_resource_type, entry.resource_type || "unknown");
      if (Number.isFinite(Number(entry.status))) increment(summary.network.by_status_class, `${Math.floor(Number(entry.status) / 100)}xx`);
      if (isFailedLogEntry(entry)) summary.network.failed += 1;
    }
  }
  return summary;
}

function normalizeLogSources(value) {
  const values = normalizeStringList(value).map((item) => item.toLowerCase());
  if (values.length === 0 || values.includes("all")) return new Set(["console", "pageerror", "network"]);
  const out = new Set();
  for (const value of values) {
    if (value === "browser") {
      out.add("console");
      out.add("pageerror");
      continue;
    }
    if (["console", "pageerror", "network"].includes(value)) out.add(value);
  }
  if (out.size === 0) return new Set(["console", "pageerror", "network"]);
  return out;
}

function normalizeStringList(value) {
  if (Array.isArray(value)) {
    return value.map((item) => String(item || "").trim()).filter(Boolean);
  }
  const text = String(value || "").trim();
  if (!text) return [];
  return text.split(",").map((item) => item.trim()).filter(Boolean);
}

function normalizeNumberList(value) {
  const raw = Array.isArray(value) ? value : normalizeStringList(value);
  return raw
    .map((item) => Math.round(Number(item)))
    .filter((item) => Number.isFinite(item));
}

function compileOptionalRegex(value, fieldName) {
  const text = String(value || "").trim();
  if (!text) return null;
  try {
    return new RegExp(text, "i");
  } catch (error) {
    throw new Error(`${fieldName} is not a valid regular expression: ${error.message}`);
  }
}

function sinceMsCutoff(value) {
  const ms = finiteNumberOrNull(value);
  if (ms === null || ms <= 0) return null;
  return Date.now() - ms;
}

function finiteNumberOrNull(value) {
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

function logUrlText(entry) {
  return [entry.url, entry.page_url, entry.response_url, entry.location?.url].filter(Boolean).join(" ");
}

function logSearchText(entry) {
  return [
    entry.source,
    entry.level,
    entry.text,
    entry.stack,
    entry.method,
    entry.url,
    entry.page_url,
    entry.resource_type,
    entry.status,
    entry.status_text,
    entry.failure_text,
    entry.response_content_type,
  ].filter((value) => value !== null && value !== undefined).join(" ");
}

function increment(object, key) {
  const normalized = String(key || "unknown");
  object[normalized] = (object[normalized] || 0) + 1;
}

function truncateLogText(value, max = 4000) {
  const text = String(value || "");
  return text.length > max ? `${text.slice(0, max)}...` : text;
}

function firstHeader(headers, name) {
  const target = String(name || "").toLowerCase();
  for (const [key, value] of Object.entries(headers || {})) {
    if (String(key).toLowerCase() === target) return String(value || "");
  }
  return null;
}

function safePageUrl(p) {
  try {
    return p?.url?.() || null;
  } catch {
    return null;
  }
}

async function pageSummary(p) {
  return {
    url: p.url(),
    title: await safeTitle(p),
  };
}

async function safeTitle(p) {
  try {
    return await p.title();
  } catch {
    return "";
  }
}

function normalizeNavigateUrl(raw) {
  let value = String(raw || "").trim();
  if (!value) throw new Error("url is required");
  if (value === "about:blank") return value;
  if (!value.includes("://")) {
    const lower = value.toLowerCase();
    const local = lower.startsWith("localhost") || lower.startsWith("127.0.0.1") || lower.startsWith("[::1]");
    value = `${local ? "http" : "https"}://${value}`;
  }
  const parsed = new URL(value);
  if (!["http:", "https:"].includes(parsed.protocol)) {
    throw new Error("Only http(s) URLs are allowed");
  }
  return parsed.toString();
}

function withMeta(payload, action) {
  return {
    ...payload,
    result_metadata: {
      integration: "browser_control",
      product: "browser",
      widget: "browser",
      action,
    },
  };
}

function normalizeConfig(input) {
  const next = {
    browserProfileDir: String(input.browserProfileDir || path.join(stateDir, "browser-profile")).trim(),
    browserHeadless: input.browserHeadless !== false,
  };
  if (!path.isAbsolute(next.browserProfileDir)) {
    next.browserProfileDir = path.resolve(stateDir, next.browserProfileDir);
  }
  return next;
}

function publicConfig() {
  return {
    browserProfileDir: config.browserProfileDir,
    browserHeadless: config.browserHeadless,
  };
}

function clampInt(value, fallback, min, max) {
  const n = Number(value);
  if (!Number.isFinite(n)) return fallback;
  return Math.max(min, Math.min(max, Math.round(n)));
}

function parseArgs(items) {
  const out = {};
  for (let i = 0; i < items.length; i += 1) {
    const item = items[i];
    if (!item.startsWith("--")) continue;
    const key = item.slice(2);
    const next = items[i + 1];
    if (next && !next.startsWith("--")) {
      out[key] = next;
      i += 1;
    } else {
      out[key] = "true";
    }
  }
  return out;
}

function loadPlaywright(nodeModuleRoot) {
  const candidates = [];
  if (nodeModuleRoot) candidates.push(path.resolve(String(nodeModuleRoot)));
  candidates.push(path.dirname(bridgeScriptPath));
  candidates.push(process.cwd());
  let lastError;
  for (const root of candidates) {
    try {
      const require = createRequire(path.join(root, "package.json"));
      return require("playwright");
    } catch (error) {
      lastError = error;
    }
  }
  throw new Error(`Cannot load playwright: ${lastError?.message || "module not found"}`);
}

async function readBody(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  const raw = Buffer.concat(chunks).toString("utf8").trim();
  if (!raw) return {};
  return JSON.parse(raw);
}

function json(res, status, body) {
  res.writeHead(status, { "content-type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(body));
}

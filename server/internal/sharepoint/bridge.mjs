#!/usr/bin/env node
import http from "node:http";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";
import { fileURLToPath } from "node:url";

const args = parseArgs(process.argv.slice(2));
const bridgeScriptPath = fileURLToPath(import.meta.url);
const browserRoot = args["browser-root"] ? path.resolve(String(args["browser-root"])) : "";
if (browserRoot) process.env.PLAYWRIGHT_BROWSERS_PATH = browserRoot;
const { chromium } = loadPlaywright(args["node-module-root"]);
const stateDir = path.resolve(args["state-dir"] || ".sharepoint-bridge");
const downloadRoot = path.join(stateDir, "downloads");
const requestedPort = Number(args.port || 0);
const maxManagedDownloadBytes = 128 * 1024 * 1024;

let config = {
  siteUrl: "",
  browserProfileDir: path.join(stateDir, "browser-profile"),
  browserHeadless: true,
  authMode: "browser",
};

let context;
let page;
let mode = "closed";
let activeBrowser = { provider: "none", name: "none" };
let loginWatcher;
let loginSwitching = false;
let siteStatus = {};
let autoLoginStatus = { state: "idle", message: "No login flow is active." };
let operationQueue = Promise.resolve();

const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url || "/", "http://127.0.0.1");
    if (req.method === "GET" && url.pathname === "/health") return json(res, 200, { ok: true });
    if (req.method === "POST" && url.pathname === "/config") return json(res, 200, await updateConfig(await readBody(req)));
    if (req.method === "GET" && url.pathname === "/status") return json(res, 200, await status());
    if (req.method === "POST" && url.pathname === "/check") return json(res, 200, await checkConnection());
    if (req.method === "POST" && url.pathname === "/login/start") return json(res, 200, await startLogin());
    if (req.method === "POST" && url.pathname === "/login/finish") return json(res, 200, await finishLogin());
    if (req.method === "POST" && url.pathname.startsWith("/tool/")) {
      const toolName = decodeURIComponent(url.pathname.slice("/tool/".length));
      return json(res, 200, await callTool(toolName, await readBody(req)));
    }
    json(res, 404, { error: "not found" });
  } catch (error) {
    json(res, 400, { error: String(error?.message || error) });
  }
});

await fs.mkdir(stateDir, { recursive: true });
await fs.mkdir(downloadRoot, { recursive: true });
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
  if (mode === "hidden" && (oldHeadless !== config.browserHeadless || oldProfile !== config.browserProfileDir)) {
    await closeBrowser();
  }
  return { ok: true, config: publicConfig() };
}

async function status() {
  const current = page;
  const site = current ? await siteStatusFromCurrentPage(current).catch(() => siteStatus) : siteStatus;
  const autoLogin =
    mode === "hidden" && autoLoginStatus.state !== "hidden"
      ? { state: "hidden", message: "Hidden SharePoint browser automation is ready." }
      : autoLoginStatus;
  return {
    running: Boolean(context && current),
    mode,
    hidden: mode === "hidden",
    url: current?.url() || null,
    title: current ? await safeTitle(current) : null,
    autoLogin,
    browser: activeBrowser,
    site,
  };
}

async function checkConnection() {
  if (mode === "login") {
    return {
      ...(await status()),
      checked: false,
      message: "Visible SharePoint login is in progress. Complete SSO before running a hidden connection check.",
    };
  }
  const p = await ensureHiddenPage();
  const site = await checkConfiguredSite(p);
  return {
    ...(await status()),
    checked: true,
    site,
    message: "Checked SharePoint using the persistent browser profile.",
  };
}

async function startLogin() {
  requireSiteUrl();
  if (config.authMode === "graph") throw new Error("Graph/MSAL SharePoint auth is not implemented in this build. Use browser auth.");
  await closeBrowser();
  const p = await launch("login");
  await p.goto(config.siteUrl, { waitUntil: "domcontentloaded" });
  startLoginWatcher();
  return {
    ...(await pageSummary(p)),
    browser: activeBrowser,
    loginWindow: "visible",
    autoDetect: "watching",
    nextStep: "Complete SharePoint SSO in the opened browser. The app will switch to hidden automation and check the site.",
  };
}

async function finishLogin() {
  requireSiteUrl();
  stopLoginWatcher();
  const visiblePage = page;
  const visibleSummary = visiblePage ? await pageSummary(visiblePage) : null;
  const lookedAuthenticated = visiblePage ? looksAuthenticated(visiblePage, config.siteUrl) : false;
  await closeBrowser();
  const p = await launch("hidden");
  await p.goto(config.siteUrl, { waitUntil: "domcontentloaded" });
  await settle(p);
  const site = await checkConfiguredSite(p);
  return {
    previousVisiblePage: { ...visibleSummary, lookedAuthenticated },
    browser: activeBrowser,
    hiddenBrowser: await pageSummary(p),
    site,
    nextStep: "Future SharePoint tool calls will use this hidden browser profile.",
  };
}

async function callTool(name, input) {
  switch (name) {
    case "sharepoint_status":
      return status();
    case "sharepoint_check_connection":
      return checkConnection();
    case "sharepoint_search":
      return sharepointSearch(input);
    case "sharepoint_get_metadata":
      return sharepointGetMetadata(input);
    case "sharepoint_read":
      return sharepointRead(input, { summarize: false });
    case "sharepoint_summarize":
      return sharepointRead(input, { summarize: true });
    case "sharepoint_download":
      return sharepointDownload(input);
    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}

async function sharepointSearch(input) {
  return withBrowserOperation(async () => {
    requireSiteUrl();
    const query = String(input?.query || "").trim();
    if (!query) throw new Error("query is required.");
    const rowLimit = clampInt(input?.row_limit ?? input?.rowLimit, 10, 1, 50);
    const extensions = arrayOfStrings(input?.file_extensions ?? input?.fileExtensions)
      .map((ext) => ext.replace(/^\./, "").toLowerCase())
      .filter(Boolean);
    const p = await ensureHiddenPage();
    await p.goto(config.siteUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const url = new URL("/_api/search/query", siteOrigin());
    url.searchParams.set("querytext", `'${query.replace(/'/g, "''")}'`);
    url.searchParams.set("rowlimit", String(rowLimit));
    url.searchParams.set("trimduplicates", "false");
    url.searchParams.set("selectproperties", "'Title,Path,Filename,FileExtension,LastModifiedTime,Author,HitHighlightedSummary,SPWebUrl,ParentLink,Size,UniqueId'");
    const result = await requestJSON(url);
    const errors = restErrors(result.status, result.json, result.text, result.loginRequired);
    let results = parseSearchResults(result.json);
    if (extensions.length) {
      const allowed = new Set(extensions);
      results = results.filter((item) => allowed.has(String(item.extension || "").replace(/^\./, "").toLowerCase()));
    }
    return withMeta({
      ok: errors.length === 0,
      status: result.status,
      url: result.url,
      query,
      rowLimit,
      results,
      resultCount: results.length,
      errors,
    }, "search");
  });
}

async function sharepointGetMetadata(input) {
  return withBrowserOperation(async () => {
    const resolved = resolveItemInput(input);
    const metadata = await getFileMetadata(resolved);
    return withMeta({
      ok: metadata.errors.length === 0,
      status: metadata.status,
      url: metadata.url,
      siteUrl: resolved.siteUrl,
      itemUrl: resolved.itemUrl,
      serverRelativeUrl: resolved.serverRelativeUrl,
      metadata: metadata.file,
      errors: metadata.errors,
    }, "metadata");
  });
}

async function sharepointRead(input, options = {}) {
  const maxChars = clampInt(input?.max_chars ?? input?.maxChars, 12000, 1000, 100000);
  const maxDownloadBytes = clampInt(input?.max_download_bytes ?? input?.maxDownloadBytes, maxManagedDownloadBytes, 1, maxManagedDownloadBytes);
  const downloaded = await downloadResolvedItem(input, { maxDownloadBytes });
  const fileName = downloaded.file?.fileName || downloaded.metadata?.name || "download";
  const mimeType = downloaded.contentType || mimeFromFilename(fileName);
  const text = isTextLikeContent(fileName, mimeType)
    ? truncateText(downloaded.fullBytes.toString("utf8"), maxChars)
    : "";
  const result = {
    ok: downloaded.ok,
    status: downloaded.status,
    url: downloaded.url,
    itemUrl: downloaded.resolved.itemUrl,
    siteUrl: downloaded.resolved.siteUrl,
    serverRelativeUrl: downloaded.resolved.serverRelativeUrl,
    metadata: downloaded.metadata,
    file: downloaded.file,
    text,
    extraction: text ? "text_preview" : "promoted_native_file",
    model_file_input: Boolean(downloaded.file),
    native_file_input: Boolean(downloaded.file),
    summary_requested: Boolean(options.summarize),
    note: text
      ? "Text preview returned. The saved file is also promoted to the app's first-class file parser."
      : "File saved for the app's first-class file parser and native model file input; summarize from the promoted file content.",
    errors: downloaded.errors,
  };
  return withMeta(result, options.summarize ? "summarize" : "read");
}

async function sharepointDownload(input) {
  const maxDownloadBytes = clampInt(input?.max_download_bytes ?? input?.maxDownloadBytes, maxManagedDownloadBytes, 1, maxManagedDownloadBytes);
  const downloaded = await downloadResolvedItem(input, { maxDownloadBytes });
  return withMeta({
    ok: downloaded.ok,
    status: downloaded.status,
    url: downloaded.url,
    itemUrl: downloaded.resolved.itemUrl,
    siteUrl: downloaded.resolved.siteUrl,
    serverRelativeUrl: downloaded.resolved.serverRelativeUrl,
    metadata: downloaded.metadata,
    file: downloaded.file,
    download: downloaded.file,
    model_file_input: Boolean(downloaded.file),
    native_file_input: Boolean(downloaded.file),
    errors: downloaded.errors,
  }, "download");
}

async function downloadResolvedItem(input, options) {
  return withBrowserOperation(async () => {
    const resolved = resolveItemInput(input);
    const metadataResult = await getFileMetadata(resolved);
    const metadata = metadataResult.file || {};
    const fileName = sanitizeManagedDownloadFileName(
      input?.filename || input?.fileName || metadata.name || fileNameFromURL(resolved.itemUrl) || path.basename(resolved.serverRelativeUrl) || "download",
    );
    const p = await ensureHiddenPage();
    await p.goto(resolved.siteUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const url = sharepointFileURL(resolved, true);
    const payload = await downloadSharePointFile(url, options);
    const errors = [
      ...metadataResult.errors,
      ...restErrors(payload.status, undefined, payload.text, payload.loginRequired),
    ];
    if (payload.downloadCapExceeded) {
      errors.push(`File exceeds managed download cap of ${options.maxDownloadBytes} bytes; local file was not saved.`);
    }
    let saved;
    if (payload.status >= 200 && payload.status < 300 && !payload.downloadCapExceeded) {
      saved = await writeManagedDownload(payload.fullBytes, {
        namespace: [siteHostLabel(), shortHash(resolved.serverRelativeUrl || resolved.itemUrl || fileName)],
        fileName,
      });
    }
    const file = saved ? {
      local_path: saved.localPath,
      localPath: saved.localPath,
      local_relative_path: saved.localRelativePath,
      localRelativePath: saved.localRelativePath,
      directory: saved.downloadDirectory,
      file_name: saved.fileName,
      fileName: saved.fileName,
      mime_type: payload.contentType || mimeFromFilename(saved.fileName),
      mimeType: payload.contentType || mimeFromFilename(saved.fileName),
      sha256: saved.sha256,
      size_bytes: saved.sizeBytes,
      sizeBytes: saved.sizeBytes,
      source: "sharepoint_managed_download",
      model_file_input: true,
      native_file_input: true,
    } : undefined;
    return {
      ok: payload.status >= 200 && payload.status < 300 && !payload.downloadCapExceeded && errors.length === 0,
      status: payload.status,
      url: payload.url,
      contentType: payload.contentType,
      fullBytes: payload.fullBytes,
      resolved,
      metadata,
      file,
      errors: [...new Set(errors)],
    };
  });
}

async function getFileMetadata(resolved) {
  const url = sharepointFileURL(resolved, false);
  const result = await requestJSON(url);
  const errors = restErrors(result.status, result.json, result.text, result.loginRequired);
  return {
    status: result.status,
    url: result.url,
    errors,
    file: summarizeFileMetadata(result.json),
  };
}

function sharepointFileURL(resolved, content) {
  const suffix = content ? "/$value" : "";
  const url = new URL(`/_api/web/GetFileByServerRelativePath(decodedUrl=@v)${suffix}`, resolved.siteUrl);
  url.searchParams.set("@v", `'${String(resolved.serverRelativeUrl || "").replace(/'/g, "''")}'`);
  return url;
}

async function requestJSON(url) {
  if (!context?.request?.get) throw new Error("Browser request context is unavailable.");
  const response = await context.request.get(url.toString(), {
    headers: { Accept: "application/json;odata=nometadata" },
    timeout: 90_000,
  });
  const text = await response.text();
  const json = parseJsonText(text);
  return {
    status: response.status(),
    url: response.url(),
    json,
    text,
    loginRequired: looksLikeLoginOrAuth(response.url(), "", text),
  };
}

async function downloadSharePointFile(url, options) {
  if (!context?.request?.get) throw new Error("Browser request context is unavailable.");
  const response = await context.request.get(url.toString(), {
    headers: { Accept: "*/*" },
    timeout: 90_000,
  });
  const headers = response.headers();
  const contentLength = parseContentLength(headers["content-length"]);
  if (contentLength > options.maxDownloadBytes) {
    return downloadedPayload({
      status: response.status(),
      url: response.url(),
      contentType: headers["content-type"] || "",
      bytesTotal: contentLength,
      fullBytes: Buffer.alloc(0),
      downloadCapExceeded: true,
      maxDownloadBytes: options.maxDownloadBytes,
      loginRequired: looksLikeLoginOrAuth(response.url(), "", ""),
    });
  }
  const body = await response.body();
  return downloadedPayload({
    status: response.status(),
    url: response.url(),
    contentType: headers["content-type"] || "",
    bytesTotal: body.length,
    fullBytes: body,
    maxDownloadBytes: options.maxDownloadBytes,
    loginRequired: looksLikeLoginOrAuth(response.url(), "", body.subarray(0, 4096).toString("utf8")),
  });
}

function downloadedPayload(input) {
  const fullBytes = Buffer.isBuffer(input.fullBytes) ? input.fullBytes : Buffer.from(input.fullBytes || []);
  const downloadCapExceeded = Boolean(input.downloadCapExceeded) || fullBytes.length > input.maxDownloadBytes;
  return {
    status: input.status,
    url: input.url,
    contentType: input.contentType || "",
    bytesTotal: input.bytesTotal ?? fullBytes.length,
    downloadCapExceeded,
    fullBytes,
    text: fullBytes.subarray(0, Math.min(fullBytes.length, 8192)).toString("utf8"),
    loginRequired: Boolean(input.loginRequired),
  };
}

async function writeManagedDownload(bytes, options = {}) {
  const namespace = Array.isArray(options.namespace) ? options.namespace.map(sanitizePathSegment).filter(Boolean) : [];
  const fileName = sanitizeManagedDownloadFileName(options.fileName || "download");
  const dir = path.join(downloadRoot, ...namespace);
  await fs.mkdir(dir, { recursive: true });
  const localPath = path.join(dir, fileName);
  const tempPath = `${localPath}.tmp-${process.pid}-${Date.now()}`;
  await fs.writeFile(tempPath, bytes, { mode: 0o600 });
  await fs.rename(tempPath, localPath);
  await fs.chmod(localPath, 0o600).catch(() => {});
  const appConfigDir = path.dirname(stateDir);
  return {
    localPath,
    localRelativePath: path.relative(appConfigDir, localPath).split(path.sep).join("/"),
    downloadDirectory: dir,
    fileName,
    sizeBytes: bytes.length,
    sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
  };
}

async function checkConfiguredSite(p) {
  try {
    await p.goto(config.siteUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const result = await requestJSON(new URL("/_api/web?$select=Title,Url", config.siteUrl));
    const json = asObject(result.json?.d || result.json);
    const title = json.Title || await safeTitle(p);
    const loginRequired = result.loginRequired || looksLikeLoginOrAuth(p.url(), title || "", result.text || "");
    const errors = restErrors(result.status, result.json, result.text, loginRequired);
    siteStatus = {
      connected: errors.length === 0,
      state: errors.length === 0 ? "connected" : "login_required",
      status: result.status,
      url: json.Url || p.url(),
      title,
      checkedAt: new Date().toISOString(),
      errors,
    };
    return siteStatus;
  } catch (error) {
    siteStatus = { connected: false, state: "error", error: String(error?.message || error), checkedAt: new Date().toISOString() };
    return siteStatus;
  }
}

async function siteStatusFromCurrentPage(p) {
  const currentUrl = p.url();
  const site = { ...siteStatus };
  if (config.siteUrl && sameHost(currentUrl, config.siteUrl)) {
    site.url = currentUrl;
    site.title = await safeTitle(p);
    site.state = looksAuthenticated(p, config.siteUrl) ? "connected" : site.state || "browser_open";
    site.connected = site.state === "connected" || site.connected === true;
  }
  siteStatus = site;
  return site;
}

function parseSearchResults(json) {
  const rows = searchRows(json);
  return rows.map((row) => {
    const cells = cellsToObject(row.Cells || row.cells || row);
    const itemUrl = stringFirst(cells.Path, cells.path, cells.Url, cells.url);
    const filename = stringFirst(cells.Filename, cells.FileName, fileNameFromURL(itemUrl), cells.Title);
    const extension = stringFirst(cells.FileExtension, filename.includes(".") ? filename.split(".").pop() : "");
    return compactObject({
      title: stringFirst(cells.Title, filename),
      filename,
      extension,
      url: itemUrl,
      webUrl: itemUrl,
      author: stringFirst(cells.Author),
      lastModifiedTime: stringFirst(cells.LastModifiedTime),
      snippet: stripHtml(stringFirst(cells.HitHighlightedSummary, cells.Summary)),
      siteUrl: stringFirst(cells.SPWebUrl, inferSiteUrlFromItemUrlSafe(itemUrl)),
      parentLink: stringFirst(cells.ParentLink),
      sizeBytes: numberValue(cells.Size),
      uniqueId: stringFirst(cells.UniqueId, cells.DocId),
      serverRelativeUrl: serverRelativeFromItemUrl(itemUrl),
    });
  }).filter((item) => item.url || item.serverRelativeUrl);
}

function searchRows(json) {
  const candidates = [
    json?.PrimaryQueryResult?.RelevantResults?.Table?.Rows,
    json?.d?.query?.PrimaryQueryResult?.RelevantResults?.Table?.Rows?.results,
    json?.d?.query?.PrimaryQueryResult?.RelevantResults?.Table?.Rows,
    json?.query?.PrimaryQueryResult?.RelevantResults?.Table?.Rows,
  ];
  for (const candidate of candidates) {
    if (Array.isArray(candidate)) return candidate;
  }
  return [];
}

function cellsToObject(cells) {
  const list = Array.isArray(cells?.results) ? cells.results : Array.isArray(cells) ? cells : [];
  const out = {};
  for (const cell of list) {
    const key = String(cell?.Key ?? cell?.key ?? "").trim();
    if (!key) continue;
    out[key] = cell?.Value ?? cell?.value;
  }
  return out;
}

function summarizeFileMetadata(json) {
  const obj = asObject(json?.d || json);
  return compactObject({
    name: stringFirst(obj.Name, obj.name),
    title: stringFirst(obj.Title, obj.title, obj.Name),
    serverRelativeUrl: stringFirst(obj.ServerRelativeUrl, obj.serverRelativeUrl),
    sizeBytes: numberValue(obj.Length, obj.length, obj.Size),
    uniqueId: stringFirst(obj.UniqueId, obj.UniqueID, obj.uniqueId),
    version: stringFirst(obj.UIVersionLabel, obj.MajorVersion ? `${obj.MajorVersion}.${obj.MinorVersion || 0}` : ""),
    createdTime: stringFirst(obj.TimeCreated, obj.Created),
    modifiedTime: stringFirst(obj.TimeLastModified, obj.Modified),
    exists: typeof obj.Exists === "boolean" ? obj.Exists : undefined,
  });
}

function resolveItemInput(input) {
  requireSiteUrl();
  const rawItemUrl = stringFirst(input?.item_url, input?.itemUrl, input?.url, input?.web_url, input?.webUrl);
  const rawServerRelative = stringFirst(input?.server_relative_url, input?.serverRelativeUrl);
  if (!rawItemUrl && !rawServerRelative) {
    throw new Error("item_url or server_relative_url is required.");
  }
  let itemUrl = "";
  let serverRelativeUrl = rawServerRelative;
  let siteUrl = normalizeSharePointURL(stringFirst(input?.site_url, input?.siteUrl, input?.spWebUrl, input?.SPWebUrl)) || config.siteUrl;
  if (rawItemUrl) {
    const parsed = new URL(rawItemUrl, config.siteUrl);
    assertAllowedSharePointUrl(parsed);
    itemUrl = parsed.toString();
    serverRelativeUrl = serverRelativeUrl || decodeURLPath(parsed.pathname);
    siteUrl = normalizeSharePointURL(stringFirst(input?.site_url, input?.siteUrl, input?.spWebUrl, input?.SPWebUrl)) || inferSiteUrlFromItemUrl(itemUrl) || config.siteUrl;
  }
  if (!serverRelativeUrl.startsWith("/")) serverRelativeUrl = `/${serverRelativeUrl}`;
  siteUrl = normalizeSharePointURL(siteUrl);
  assertAllowedSharePointUrl(new URL(siteUrl));
  return { itemUrl, serverRelativeUrl, siteUrl };
}

function inferSiteUrlFromItemUrl(rawUrl) {
  if (!rawUrl) return "";
  const url = new URL(rawUrl, config.siteUrl);
  assertAllowedSharePointUrl(url);
  const parts = url.pathname.split("/").filter(Boolean);
  if (parts.length >= 2 && ["sites", "teams", "personal"].includes(parts[0].toLowerCase())) {
    return `${url.origin}/${parts[0]}/${parts[1]}`;
  }
  const configured = new URL(config.siteUrl);
  if (configured.pathname && configured.pathname !== "/") return config.siteUrl;
  return url.origin;
}

function inferSiteUrlFromItemUrlSafe(rawUrl) {
  try {
    return inferSiteUrlFromItemUrl(rawUrl);
  } catch {
    return "";
  }
}

function serverRelativeFromItemUrl(rawUrl) {
  if (!rawUrl) return undefined;
  try {
    return decodeURLPath(new URL(rawUrl, config.siteUrl).pathname);
  } catch {
    return undefined;
  }
}

function assertAllowedSharePointUrl(target) {
  const base = new URL(config.siteUrl);
  if (target.hostname !== base.hostname || target.protocol !== base.protocol) {
    throw new Error(`Blocked URL outside configured SharePoint site: ${target.toString()}`);
  }
}

function siteOrigin() {
  return new URL(config.siteUrl).origin;
}

function siteHostLabel() {
  return sanitizePathSegment(new URL(config.siteUrl).hostname);
}

function requireSiteUrl() {
  if (!config.siteUrl) throw new Error("Configure SharePoint site URL before connecting SharePoint.");
}

async function withBrowserOperation(fn) {
  const run = operationQueue.then(fn, fn);
  operationQueue = run.then(() => undefined, () => undefined);
  return run;
}

async function ensureHiddenPage() {
  if (!context || mode !== "hidden") {
    await closeBrowser();
    return launch("hidden");
  }
  page = page || context.pages()[0] || await context.newPage();
  return page;
}

async function launch(nextMode) {
  if (!context) {
    await fs.mkdir(config.browserProfileDir, { recursive: true });
    context = await chromium.launchPersistentContext(config.browserProfileDir, {
      headless: nextMode === "hidden" ? config.browserHeadless : false,
      viewport: { width: 1440, height: 1000 },
      chromiumSandbox: true,
    });
    activeBrowser = { provider: "bundled", source: "configured", name: "Bundled Chromium" };
    context.on("page", (p) => { page = p; });
  }
  mode = nextMode;
  page = page || context.pages()[0] || await context.newPage();
  return page;
}

async function closeBrowser() {
  stopLoginWatcher();
  await context?.close().catch(() => {});
  context = undefined;
  page = undefined;
  mode = "closed";
  activeBrowser = { provider: "none", name: "none" };
}

function startLoginWatcher() {
  stopLoginWatcher();
  const deadline = Date.now() + 15 * 60 * 1000;
  autoLoginStatus = { state: "watching", message: "Waiting for SharePoint SSO to complete in the visible browser.", startedAt: new Date().toISOString() };
  loginWatcher = setInterval(() => { void checkLoginCompletion(deadline); }, 1500);
}

function stopLoginWatcher() {
  if (loginWatcher) clearInterval(loginWatcher);
  loginWatcher = undefined;
}

async function checkLoginCompletion(deadline) {
  if (loginSwitching) return;
  if (mode !== "login" || !page) {
    stopLoginWatcher();
    return;
  }
  if (Date.now() > deadline) {
    autoLoginStatus = { state: "expired", message: "Timed out waiting for SharePoint login. Start login again if needed.", updatedAt: new Date().toISOString() };
    stopLoginWatcher();
    return;
  }
  try {
    if (!looksAuthenticated(page, config.siteUrl)) {
      autoLoginStatus = { ...autoLoginStatus, state: "watching", currentUrl: page.url(), updatedAt: new Date().toISOString() };
      return;
    }
    await autoSwitchToHidden();
  } catch (error) {
    autoLoginStatus = { state: "error", message: String(error?.message || error), updatedAt: new Date().toISOString() };
    stopLoginWatcher();
  }
}

async function autoSwitchToHidden() {
  loginSwitching = true;
  autoLoginStatus = { state: "switching", message: "SharePoint login detected. Closing visible browser, starting hidden automation, and checking the site.", updatedAt: new Date().toISOString() };
  try {
    const visiblePage = page ? await pageSummary(page) : null;
    await closeBrowser();
    const p = await launch("hidden");
    await p.goto(config.siteUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const site = await checkConfiguredSite(p);
    autoLoginStatus = { state: "hidden", message: "SharePoint login detected. Hidden browser automation is ready.", visiblePage, browser: activeBrowser, hiddenBrowser: await pageSummary(p), site, updatedAt: new Date().toISOString() };
  } finally {
    loginSwitching = false;
  }
}

function looksAuthenticated(p, baseUrl) {
  try {
    const current = new URL(p.url());
    const base = new URL(baseUrl);
    return current.hostname === base.hostname && !looksLikeLoginOrAuth(current.toString(), "", "");
  } catch {
    return false;
  }
}

function looksLikeLoginOrAuth(url, title, text) {
  const haystack = `${url}\n${title}\n${String(text || "").slice(0, 2000)}`.toLowerCase();
  return /\/login|login\.microsoftonline\.com|login\.live\.com|microsoftonline|sharepoint\.com\/_forms|single sign|sign in|log in|verify your identity|saml|oauth/.test(haystack);
}

async function pageSummary(p, includeText = false) {
  return {
    url: p.url(),
    title: await safeTitle(p),
    text: includeText ? truncateText(await safeBodyText(p)) : undefined,
  };
}

async function settle(p) {
  await p.waitForLoadState("domcontentloaded", { timeout: 15000 }).catch(() => {});
  await p.waitForLoadState("networkidle", { timeout: 2500 }).catch(() => {});
  await p.waitForTimeout(250).catch(() => {});
}

async function safeTitle(p) {
  try { return await p.title(); } catch { return null; }
}

async function safeBodyText(p) {
  try { return await p.locator("body").innerText({ timeout: 5000 }); } catch { return ""; }
}

function normalizeConfig(raw) {
  return {
    siteUrl: normalizeSharePointURL(raw.siteUrl || ""),
    browserProfileDir: path.resolve(String(raw.browserProfileDir || path.join(stateDir, "browser-profile"))),
    browserHeadless: raw.browserHeadless !== false,
    authMode: String(raw.authMode || "browser").trim().toLowerCase() === "graph" ? "graph" : "browser",
  };
}

function publicConfig() {
  return { ...config, browserProfileDir: config.browserProfileDir };
}

function normalizeSharePointURL(raw) {
  const value = String(raw || "").trim().replace(/\/+$/, "");
  if (!value) return "";
  const url = new URL(value.includes("://") ? value : `https://${value}`);
  if (!["http:", "https:"].includes(url.protocol)) throw new Error("SharePoint URL must use http or https.");
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/+$/, "");
}

function restErrors(status, json, text, loginRequired) {
  const errors = [];
  if (loginRequired) errors.push("SharePoint login is required.");
  if (status === 401 || status === 403) errors.push(`SharePoint returned HTTP ${status}; check account access to this site or file.`);
  else if (status && status >= 400) errors.push(`HTTP ${status}`);
  const obj = asObject(json?.error || json?.odata?.error || json);
  if (typeof obj.message === "string" && status && status >= 400) errors.push(obj.message);
  if (typeof obj.message?.value === "string" && status && status >= 400) errors.push(obj.message.value);
  if (!json && status && status >= 400 && text) errors.push(truncateText(text, 500));
  return [...new Set(errors)];
}

function withMeta(result, widget) {
  return {
    ...result,
    result_metadata: {
      integration: "sharepoint",
      product: "sharepoint",
      widget,
    },
  };
}

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith("--")) continue;
    out[arg.slice(2)] = argv[i + 1];
    i += 1;
  }
  return out;
}

function loadPlaywright(nodeModuleRoot) {
  const candidates = [];
  if (nodeModuleRoot) candidates.push(path.join(path.resolve(String(nodeModuleRoot)), "package.json"));
  candidates.push(path.join(process.cwd(), "package.json"));
  candidates.push(bridgeScriptPath);

  const failures = [];
  for (const candidate of candidates) {
    try {
      return createRequire(candidate)("playwright");
    } catch (error) {
      failures.push(`${candidate}: ${error?.message || error}`);
    }
  }
  throw new Error(`Playwright package is required for SharePoint browser automation. Install dependencies with npm install. ${failures.join(" | ")}`);
}

async function readBody(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  const text = Buffer.concat(chunks).toString("utf8").trim();
  if (!text) return {};
  return JSON.parse(text);
}

function json(res, status, value) {
  const b = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { "Content-Type": "application/json", "Content-Length": b.length });
  res.end(b);
}

function asObject(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function arrayOfStrings(value) {
  return Array.isArray(value)
    ? value.map((item) => String(item || "").trim()).filter(Boolean)
    : typeof value === "string"
      ? value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean)
      : [];
}

function clampInt(value, fallback, min, max) {
  const parsed = Number.parseInt(String(value ?? ""), 10);
  if (!Number.isFinite(parsed)) return fallback;
  return Math.max(min, Math.min(max, parsed));
}

function parseJsonText(text) {
  const trimmed = String(text || "").trim();
  if (!trimmed) return undefined;
  try { return JSON.parse(trimmed); } catch {}
  const match = trimmed.match(/^[\s\S]*?({[\s\S]*}|\[[\s\S]*\])[\s\S]*$/);
  if (match) {
    try { return JSON.parse(match[1]); } catch {}
  }
  return undefined;
}

function truncateText(text, maxChars = 12000) {
  const collapsed = String(text || "").replace(/\n{4,}/g, "\n\n\n").trim();
  return collapsed.length <= maxChars ? collapsed : `${collapsed.slice(0, maxChars)}\n\n[truncated ${collapsed.length - maxChars} chars]`;
}

function stripHtml(html) {
  return String(html || "").replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
}

function compactObject(obj) {
  return Object.fromEntries(Object.entries(obj).filter(([, value]) => value !== undefined && value !== null && value !== "" && (!Array.isArray(value) || value.length > 0)));
}

function stringFirst(...values) {
  for (const value of values) {
    const text = String(value ?? "").trim();
    if (text) return text;
  }
  return "";
}

function numberValue(...values) {
  for (const value of values) {
    const n = Number(value);
    if (Number.isFinite(n) && n >= 0) return n;
  }
  return undefined;
}

function parseContentLength(raw) {
  const parsed = Number.parseInt(String(raw || ""), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

function sanitizePathSegment(raw) {
  return String(raw || "")
    .replace(/[<>:"/\\|?*\u0000-\u001f]/g, "-")
    .replace(/\s+/g, " ")
    .replace(/^\.+$/, "")
    .trim()
    .slice(0, 120);
}

function sanitizeManagedDownloadFileName(raw) {
  const value = String(raw || "").replace(/\\/g, "/").split("/").pop() || "";
  return value.replace(/\0/g, "").trim().replace(/^\.+$/, "").slice(0, 180) || "download";
}

function fileNameFromURL(rawUrl) {
  if (!rawUrl) return "";
  try {
    return decodeURIComponent(new URL(rawUrl, config.siteUrl).pathname.split("/").pop() || "");
  } catch {
    return "";
  }
}

function decodeURLPath(pathname) {
  try {
    return decodeURIComponent(pathname);
  } catch {
    return pathname;
  }
}

function shortHash(value) {
  return crypto.createHash("sha256").update(String(value || "")).digest("hex").slice(0, 16);
}

function sameHost(a, b) {
  try { return new URL(a).hostname === new URL(b).hostname; } catch { return false; }
}

function mimeFromFilename(fileName) {
  const ext = path.extname(String(fileName || "")).toLowerCase();
  switch (ext) {
    case ".pdf": return "application/pdf";
    case ".docx": return "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
    case ".pptx": return "application/vnd.openxmlformats-officedocument.presentationml.presentation";
    case ".xlsx": return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";
    case ".csv": return "text/csv";
    case ".txt": return "text/plain";
    case ".md": return "text/markdown";
    case ".json": return "application/json";
    default: return "application/octet-stream";
  }
}

function isTextLikeContent(fileName, mimeType) {
  const mime = String(mimeType || "").toLowerCase();
  if (mime.startsWith("text/") || /json|xml|csv|yaml|markdown|javascript|typescript|sql/.test(mime)) return true;
  return /\.(txt|md|markdown|json|csv|tsv|xml|yaml|yml|log|sql|js|jsx|ts|tsx|go|py|java|rb|rs|c|cc|cpp|h|hpp|css|html)$/i.test(String(fileName || ""));
}

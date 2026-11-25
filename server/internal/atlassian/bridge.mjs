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
const stateDir = path.resolve(args["state-dir"] || ".atlassian-bridge");
const downloadRoot = path.join(stateDir, "downloads");
const requestedPort = Number(args.port || 0);
const maxManagedDownloadBytes = 128 * 1024 * 1024;

let config = {
  jiraBaseUrl: "",
  confluenceBaseUrl: "",
  defaultJiraProjectKey: "",
  allowedProjects: [],
  allowedAuthDomains: ["id.atlassian.com", "auth.atlassian.com", "login.microsoftonline.com"],
  browserProvider: "bundled",
  browserChannel: "",
  browserExecutablePath: "",
  browserProfileDir: path.join(stateDir, "browser-profile"),
  browserHeadless: true,
};

let context;
let page;
let mode = "closed";
let activeBrowser = { provider: "none", name: "none" };
let loginWatcher;
let loginSwitching = false;
let productStatus = {};
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
  const oldProvider = config.browserProvider;
  const oldChannel = config.browserChannel;
  const oldExecutablePath = config.browserExecutablePath;
  config = normalizeConfig({ ...config, ...(next || {}) });
  await fs.mkdir(config.browserProfileDir, { recursive: true });
  if (
    mode === "hidden"
    && (
      oldHeadless !== config.browserHeadless
      || oldProfile !== config.browserProfileDir
      || oldProvider !== config.browserProvider
      || oldChannel !== config.browserChannel
      || oldExecutablePath !== config.browserExecutablePath
    )
  ) {
    await closeBrowser();
  }
  return { ok: true, config: publicConfig() };
}

async function status() {
  const current = page;
  const products = current ? await productStatusFromCurrentPage(current).catch(() => productStatus) : productStatus;
  const autoLogin =
    mode === "hidden" && autoLoginStatus.state !== "hidden"
      ? { state: "hidden", message: "Hidden browser automation is ready." }
      : autoLoginStatus;
  return {
    running: Boolean(context && current),
    mode,
    hidden: mode === "hidden",
    url: current?.url() || null,
    title: current ? await safeTitle(current) : null,
    autoLogin,
    browser: activeBrowser,
    products,
  };
}

async function checkConnection() {
  if (mode === "login") {
    return {
      ...(await status()),
      checked: false,
      message: "Visible login is in progress. Complete SSO before running a hidden connection check.",
    };
  }
  const p = await ensureHiddenPage();
  const products = await checkConfiguredProducts(p);
  return {
    ...(await status()),
    checked: true,
    products,
    message: "Checked Jira and Confluence using the persistent Atlassian browser profile.",
  };
}

async function startLogin() {
  requireJiraBaseUrl();
  await closeBrowser();
  const p = await launch("login");
  await p.goto(config.jiraBaseUrl, { waitUntil: "domcontentloaded" });
  startLoginWatcher();
  return {
    ...(await pageSummary(p)),
    browser: activeBrowser,
    loginWindow: "visible",
    autoDetect: "watching",
    unified: true,
    nextStep: "Complete Atlassian SSO in the opened browser. The app will switch to hidden automation and check Jira plus Confluence.",
  };
}

async function finishLogin() {
  requireJiraBaseUrl();
  stopLoginWatcher();
  const visiblePage = page;
  const visibleSummary = visiblePage ? await pageSummary(visiblePage) : null;
  const lookedAuthenticated = visiblePage ? looksAuthenticated(visiblePage, config.jiraBaseUrl) : false;
  await closeBrowser();
  const p = await launch("hidden");
  await p.goto(config.jiraBaseUrl, { waitUntil: "domcontentloaded" });
  await settle(p);
  const products = await checkConfiguredProducts(p);
  return {
    previousVisiblePage: { ...visibleSummary, lookedAuthenticated },
    browser: activeBrowser,
    hiddenBrowser: await pageSummary(p),
    products,
    nextStep: "Future Jira and Confluence tool calls will use this hidden browser profile.",
  };
}

async function callTool(name, input) {
  switch (name) {
    case "atlassian_status":
      return status();
    case "atlassian_check_connection":
      return checkConnection();
    case "jira_get_issue":
      return jiraGetIssue(input);
    case "jira_get_issue_history":
      return jiraGetIssueHistory(input);
    case "jira_get_issue_comments":
      return jiraGetIssueComments(input);
    case "jira_get_issue_attachments":
      return jiraGetIssueAttachments(input);
    case "jira_read_attachment":
      return jiraReadAttachment(input);
    case "jira_get_issue_link_types":
      return jiraGetIssueLinkTypes(input);
    case "jira_find_users":
      return jiraFindUsers(input);
    case "jira_search_issues":
      return jiraSearchIssues(input);
    case "jira_find":
      return jiraFind(input);
    case "jira_rest_get":
      return jiraRestGetSummary(input);
    case "confluence_search":
      return confluenceSearch(input);
    case "confluence_open_page":
      return confluenceOpenPage(input);
    case "confluence_open_space":
      return confluenceOpenSpace(input);
    case "confluence_rest_get":
      return confluenceRestGetSummary(input);
    case "jira_add_comment_execute":
      return jiraAddComment(input);
    case "jira_add_related_tickets_execute":
      return jiraAddRelatedTickets(input);
    case "jira_upload_attachment_execute":
      return jiraUploadAttachment(input);
    case "jira_update_issue_execute":
      return jiraUpdateIssue(input);
    case "jira_create_issue_execute":
      return jiraCreateIssue(input);
    default:
      throw new Error(`Unknown tool: ${name}`);
  }
}

async function jiraGetIssue(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const fields =
    String(input?.fields || "").trim() ||
    "summary,description,status,issuetype,priority,assignee,reporter,created,updated,parent,components,labels,attachment,issuelinks,comment,customfield_10020";
  const expand = String(input?.expand || "").trim() || undefined;
  const result = await jiraRestGet(`/rest/api/3/issue/${encodeURIComponent(issueKey)}`, { fields, expand });
  const json = asObject(result.json);
  return withMeta({
    ...restMeta(result),
    issue: summarizeJiraIssue(json),
    changelog: summarizeJiraChangelog(json),
    rawIncluded: false,
  }, "jira", "issue");
}

async function jiraGetIssueHistory(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const result = await jiraRestGet(`/rest/api/3/issue/${encodeURIComponent(issueKey)}`, {
    expand: "changelog",
    fields: "summary,parent,components,attachment,issuelinks,customfield_10020",
  });
  const transitions = summarizeSprintTransitions(asObject(result.json));
  return withMeta({
    ...restMeta(result),
    issue: summarizeJiraIssue(asObject(result.json)),
    sprintTransitions: transitions,
    previousSprint: transitions[0]?.fromSprintName ? { id: transitions[0]?.fromSprintId, name: transitions[0]?.fromSprintName } : undefined,
    currentSprint: transitions[0]?.toSprintName ? { id: transitions[0]?.toSprintId, name: transitions[0]?.toSprintName } : undefined,
  }, "jira", "history");
}

async function jiraGetIssueComments(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const maxResults = clampInt(input?.maxResults, 25, 1, 100);
  const requestedOrder = String(input?.orderBy || "created").trim();
  const orderBy = ["created", "-created", "+created"].includes(requestedOrder) ? requestedOrder : "created";
  const result = await jiraRestGet(`/rest/api/3/issue/${encodeURIComponent(issueKey)}/comment`, {
    maxResults: String(maxResults),
    orderBy,
    expand: "renderedBody",
  });
  const json = asObject(result.json);
  const comments = arrayOfObjects(json.comments).map((comment) => summarizeJiraComment(issueKey, comment));
  return withMeta({
    ...restMeta(result),
    issueKey,
    comments,
    total: json.total,
    startAt: json.startAt,
    maxResults: json.maxResults,
    orderBy,
  }, "jira", "comment_list");
}

async function jiraGetIssueAttachments(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const result = await jiraRestGet(`/rest/api/3/issue/${encodeURIComponent(issueKey)}`, {
    fields: "summary,attachment",
  });
  const issue = asObject(result.json);
  const fields = asObject(issue.fields);
  return withMeta({
    ...restMeta(result),
    issue: compactObject({
      id: issue.id,
      key: issue.key,
      url: issue.key && config.jiraBaseUrl ? new URL(`/browse/${issue.key}`, config.jiraBaseUrl).toString() : undefined,
      summary: fields.summary,
    }),
    issueKey,
    attachments: arrayOfObjects(fields.attachment).map(summarizeJiraAttachment),
    total: arrayOfObjects(fields.attachment).length,
  }, "jira", "attachment_list");
}

async function jiraReadAttachment(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const attachmentId = assertAttachmentId(String(input?.attachmentId || input?.id || ""));
  const attachmentsResult = await jiraGetIssueAttachments({ issueKey });
  const attachment = arrayOfObjects(attachmentsResult.attachments).find((item) => String(item.id || "") === attachmentId);
  if (!attachment) throw new Error(`Attachment ${attachmentId} was not found on ${issueKey}.`);
  const attachmentMimeType = String(attachment.mimeType || "").toLowerCase();
  const fileName = String(attachment.fileName || "");
  const imageLike = isVisionImageContent(fileName, attachmentMimeType);
  const maxBytes = clampInt(
    input?.maxBytes,
    imageLike ? 10 * 1024 * 1024 : 64 * 1024,
    1,
    imageLike ? 10 * 1024 * 1024 : 1024 * 1024,
  );
  const maxDownloadBytes = clampInt(input?.maxDownloadBytes, maxManagedDownloadBytes, 1, maxManagedDownloadBytes);
  const includeBase64 = input?.includeBase64 === true || imageLike;
  const result = await jiraRestDownload(`/rest/api/3/attachment/content/${encodeURIComponent(attachmentId)}`, {
    maxBytes,
    maxDownloadBytes,
    saveFileName: fileName || `jira-attachment-${attachmentId}`,
    downloadNamespace: ["jira", issueKey, attachmentId],
  });
  const contentType = String(result.contentType || attachment.mimeType || "").toLowerCase();
  const textLike = isTextLikeContent(fileName, contentType);
  const out = {
    ...restMeta(result),
    issueKey,
    attachment,
    attachmentId,
    fileName,
    mimeType: contentType || attachment.mimeType,
    sizeBytes: attachment.sizeBytes,
    bytesReturned: result.bytesReturned,
    truncated: result.truncated,
    binary: !textLike,
    localPath: result.localPath,
    local_path: result.localPath,
    localRelativePath: result.localRelativePath,
    local_relative_path: result.localRelativePath,
    downloadDirectory: result.downloadDirectory,
    download_directory: result.downloadDirectory,
    download: result.download,
    sha256: result.sha256,
    downloadMethod: result.downloadMethod,
  };
  if (textLike) {
    out.content = result.text;
  } else if (includeBase64) {
    out.contentBase64 = result.base64;
  } else {
    out.note = "Attachment is binary or not text-like. Re-run with includeBase64=true only if bounded base64 content is required.";
  }
  return withMeta(out, "jira", "attachment");
}

function isVisionImageContent(fileName, contentType) {
  const mediaType = String(contentType || "").split(";")[0].trim().toLowerCase();
  if (["image/png", "image/jpeg", "image/jpg", "image/webp", "image/gif"].includes(mediaType)) return true;
  const ext = String(fileName || "").toLowerCase().split("?")[0].match(/\.([a-z0-9]+)$/)?.[1] || "";
  return ["png", "jpg", "jpeg", "webp", "gif"].includes(ext);
}

async function jiraGetIssueLinkTypes() {
  const result = await jiraRestGet("/rest/api/3/issueLinkType");
  const json = asObject(result.json);
  return withMeta({
    ...restMeta(result),
    issueLinkTypes: arrayOfObjects(json.issueLinkTypes).map((item) => compactObject({
      id: item.id,
      name: item.name,
      inward: item.inward,
      outward: item.outward,
      self: item.self,
    })),
  }, "jira", "issue_link_types");
}

async function jiraFindUsers(input) {
  const query = String(input?.query || "").trim();
  if (!query) throw new Error("query is required");
  const maxResults = clampInt(input?.maxResults, 25, 1, 50);
  const result = await jiraRestGet("/rest/api/3/user/search", { query, maxResults: String(maxResults) });
  const users = Array.isArray(result.json) ? result.json.map(summarizeUser).filter(Boolean) : [];
  return withMeta({
    ...restMeta(result),
    query,
    users,
    total: users.length,
  }, "jira", "user_list");
}

async function jiraSearchIssues(input) {
  const jql = assertAllowedJql(String(input?.jql || ""));
  const result = await jiraRestGet("/rest/api/3/search/jql", {
    jql,
    maxResults: "25",
    fields: "summary,status,issuetype,assignee,reporter,created,updated,parent,components,labels,customfield_10020",
  });
  const json = asObject(result.json);
  const issues = arrayOfObjects(json.issues).map(summarizeJiraIssue);
  return withMeta({
    ...restMeta(result),
    jql,
    issueKeys: issues.map((issue) => issue.key).filter(Boolean),
    issues,
    total: json.total,
    startAt: json.startAt,
    maxResults: json.maxResults,
  }, "jira", "issue_list");
}

async function jiraFind(input) {
  const type = String(input?.type || "all");
  const query = String(input?.query || "").trim();
  const projectOrBoard = String(input?.projectOrBoard || "").trim();
  if (!query && !projectOrBoard) throw new Error("Provide query, projectOrBoard, or both.");
  const result = {};
  if (type === "project" || type === "all") {
    if (!query) throw new Error("Project discovery requires query.");
    result.projects = await findProjects(query);
  }
  if (type === "board" || type === "all") {
    if (!query) throw new Error("Board discovery requires query.");
    result.boards = await findBoards(query);
  }
  if (type === "issue" || type === "all") {
    const clauses = [];
    if (projectOrBoard) clauses.push(`project = ${jqlValue(projectOrBoard)}`);
    if (query && type === "issue") clauses.push(`text ~ ${jqlValue(query)}`);
    const orderBy = ["created", "updated", "key"].includes(input?.orderBy) ? input.orderBy : "updated";
    const direction = String(input?.direction || "DESC").toUpperCase() === "ASC" ? "ASC" : "DESC";
    const jql = clauses.length ? `${clauses.join(" AND ")} ORDER BY ${orderBy} ${direction}` : `text ~ ${jqlValue(query)} ORDER BY updated DESC`;
    result.issues = await jiraSearchIssues({ jql });
  }
  return withMeta({ type, query, projectOrBoard, ...result }, "jira", "find");
}

async function findProjects(query) {
  const result = await jiraRestGet("/rest/api/3/project/search", { query, maxResults: "25" });
  return {
    ...restMeta(result),
    query,
    candidates: arrayOfObjects(asObject(result.json).values).map(summarizeJiraProject),
    total: asObject(result.json).total,
  };
}

async function findBoards(query) {
  const result = await jiraRestGet("/rest/agile/1.0/board", { name: query, maxResults: "25" });
  return {
    ...restMeta(result),
    query,
    candidates: arrayOfObjects(asObject(result.json).values).map(summarizeJiraBoard),
  };
}

async function jiraRestGetSummary(input) {
  const result = await jiraRestGet(String(input?.path || ""));
  return withMeta({ ...result, text: truncateText(result.text, 4000) }, "jira", "rest");
}

async function confluenceSearch(input) {
  const query = String(input?.query || "").trim();
  if (!query) throw new Error("query is required");
  const spaceKey = String(input?.spaceKey || "").trim().toUpperCase();
  const cql = spaceKey ? `type = page AND space = "${cqlEscape(spaceKey)}" AND text ~ "${cqlEscape(query)}"` : `type = page AND text ~ "${cqlEscape(query)}"`;
  const result = await confluenceRestGet("/rest/api/search", { cql, limit: "25" });
  const json = asObject(result.json);
  return withMeta({
    ...restMeta(result),
    query,
    spaceKey: spaceKey || undefined,
    cql,
    pages: arrayOfObjects(json.results).map(summarizeConfluenceSearchResult),
    totalSize: json.totalSize,
    limit: json.limit,
    start: json.start,
  }, "confluence", "page_list");
}

async function confluenceOpenSpace(input) {
  const key = String(input?.spaceKey || "").trim();
  if (!key) throw new Error("spaceKey is required");
  const result = await confluenceRestGet("/api/v2/spaces", {
    keys: key,
    limit: "25",
    "description-format": "plain",
    "include-icon": "true",
  });
  const spaces = arrayOfObjects(asObject(result.json).results).map(summarizeConfluenceSpace);
  const space = spaces.find((candidate) => String(candidate.key || "").toUpperCase() === key.toUpperCase()) || spaces[0];
  let homepage;
  if (space?.homepageId) {
    homepage = await confluenceOpenPage({ pageId: String(space.homepageId) }).catch(() => undefined);
  }
  return withMeta({ ...restMeta(result), query: key, space, spaces, homepage }, "confluence", "space");
}

async function confluenceOpenPage(input) {
  const pageId = String(input?.pageId || "").trim() || extractConfluencePageId(String(input?.url || ""));
  const bodyFormat = input?.bodyFormat === "atlas_doc_format" ? "atlas_doc_format" : "storage";
  if (pageId) {
    const result = await confluenceRestGet(`/api/v2/pages/${encodeURIComponent(pageId)}`, {
      "body-format": bodyFormat,
      "include-labels": "true",
      "include-version": "true",
    });
    return withMeta({ ...restMeta(result), page: summarizeConfluencePage(asObject(result.json)), rawIncluded: false }, "confluence", "page");
  }
  const title = String(input?.title || "").trim();
  if (!title) throw new Error("Provide pageId, url containing a page id, or title.");
  const params = { title, "body-format": bodyFormat, limit: "25" };
  if (input?.spaceKey) {
    const space = await confluenceOpenSpace({ spaceKey: String(input.spaceKey) });
    const id = asObject(space.space).id;
    if (id) params["space-id"] = String(id);
  }
  const result = await confluenceRestGet("/api/v2/pages", params);
  const pages = arrayOfObjects(asObject(result.json).results).map(summarizeConfluencePage);
  return withMeta({ ...restMeta(result), title, spaceKey: input?.spaceKey, pages, page: pages[0], rawIncluded: false }, "confluence", "page_list");
}

async function confluenceRestGetSummary(input) {
  const result = await confluenceRestGet(String(input?.path || ""));
  return withMeta({ ...result, text: truncateText(result.text, 4000), summary: summarizeConfluenceJson(result.json) }, "confluence", "rest");
}

async function jiraAddComment(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const comment = String(input?.comment || "").trim();
  if (!comment) throw new Error("comment is required");
  const replyToCommentId = String(input?.replyToCommentId || input?.parentId || input?.commentId || "").trim();
  const mentions = normalizeMentionRefs(input?.mentions);
  const doc = adfDoc(comment, mentions);
  if (replyToCommentId) {
    doc.content.unshift(replyReferenceBlock(issueKey, replyToCommentId, String(input?.replyContextLabel || "")));
  }
  const body = { body: doc };
  if (replyToCommentId) {
    body.parentId = replyToCommentId;
    body.properties = [{
      key: "tributary.replyTo",
      value: { issueKey, parentCommentId: replyToCommentId },
    }];
  }
  const result = await jiraRestPost(`/rest/api/3/issue/${encodeURIComponent(issueKey)}/comment`, body);
  const createdCommentId = asObject(result.json).id;
  return withMeta({
    ...restMeta(result),
    issueKey,
    comment,
    commentId: createdCommentId,
    commentUrl: createdCommentId ? commentUrl(issueKey, createdCommentId) : undefined,
    replyToCommentId: replyToCommentId || undefined,
    replyToUrl: replyToCommentId ? commentUrl(issueKey, replyToCommentId) : undefined,
    mentions,
    bodyFormat: "jira_adf",
  }, "jira", "write_result");
}

async function jiraAddRelatedTickets(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const relationship = String(input?.relationship || "Relates").trim() || "Relates";
  const relatedIssueKeys = arrayOfStrings(input?.relatedIssueKeys)
    .map((key) => assertAllowedIssueKey(key))
    .filter((key, index, arr) => key !== issueKey && arr.indexOf(key) === index);
  if (!relatedIssueKeys.length) throw new Error("relatedIssueKeys must include at least one issue different from issueKey.");
  const comment = String(input?.comment || "").trim();
  const links = [];
  for (const relatedIssueKey of relatedIssueKeys) {
    const body = {
      type: { name: relationship },
      inwardIssue: { key: issueKey },
      outwardIssue: { key: relatedIssueKey },
    };
    if (comment) body.comment = { body: adfDoc(comment) };
    const result = await jiraRestPost("/rest/api/3/issueLink", body);
    links.push({
      issueKey,
      relatedIssueKey,
      relationship,
      ok: result.ok,
      status: result.status,
      url: result.url,
      errors: result.errors || [],
    });
  }
  return withMeta({
    ok: links.every((link) => link.ok),
    issueKey,
    relatedIssueKeys,
    relationship,
    links,
    errors: links.flatMap((link) => link.errors || []),
  }, "jira", "write_result");
}

async function jiraUploadAttachment(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const fileName = sanitizeAttachmentFileName(String(input?.fileName || ""));
  if (!fileName) throw new Error("fileName is required");
  const mimeType = String(input?.mimeType || "application/octet-stream").trim() || "application/octet-stream";
  const contentBase64 = String(input?.contentBase64 || "").trim();
  if (!contentBase64) throw new Error("contentBase64 is required");
  const result = await jiraRestUploadAttachment(`/rest/api/3/issue/${encodeURIComponent(issueKey)}/attachments`, {
    fileName,
    mimeType,
    contentBase64,
  });
  const attachments = Array.isArray(result.json) ? result.json.map(summarizeJiraAttachment) : [];
  return withMeta({
    ...restMeta(result),
    issueKey,
    fileName,
    mimeType,
    sha256: String(input?.sha256 || "").trim() || undefined,
    attachments,
  }, "jira", "write_result");
}

async function jiraCreateIssue(input) {
  const projectKey = assertAllowedProjectKey(String(input?.projectKey || config.defaultJiraProjectKey || ""));
  const summary = String(input?.summary || "").trim();
  if (!summary) throw new Error("summary is required");
  const description = String(input?.description || "").trim();
  const issueType = String(input?.issueType || "Task").trim() || "Task";
  const mentions = normalizeMentionRefs(input?.mentions);
  const components = dedupeStrings(arrayOfStrings(input?.components));
  const fields = {
    project: { key: projectKey },
    summary,
    issuetype: { name: issueType },
  };
  if (components.length) fields.components = jiraComponentRefs(components);
  if (description) fields.description = adfDoc(description, mentions);
  const result = await jiraRestPost("/rest/api/3/issue", { fields });
  return withMeta({
    ...restMeta(result),
    projectKey,
    issueKey: asObject(result.json).key,
    issueId: asObject(result.json).id,
    issueUrl: asObject(result.json).key ? new URL(`/browse/${asObject(result.json).key}`, config.jiraBaseUrl).toString() : undefined,
    summary,
    issueType,
    description: description || undefined,
    components,
    mentions,
    bodyFormat: description ? "jira_adf" : undefined,
  }, "jira", "write_result");
}

async function jiraUpdateIssue(input) {
  const issueKey = assertAllowedIssueKey(String(input?.issueKey || ""));
  const mentions = normalizeMentionRefs(input?.mentions);
  const fields = { ...asObject(input?.fields) };
  const summary = String(input?.summary || "").trim();
  const description = String(input?.description || "").trim();
  const issueType = String(input?.issueType || "").trim();
  const assigneeAccountId = String(input?.assigneeAccountId || "").trim();
  const priorityName = String(input?.priorityName || "").trim();
  const labels = arrayOfStrings(input?.labels);
  const components = dedupeStrings(arrayOfStrings(input?.components));

  if (typeof fields.description === "string") {
    const customDescription = fields.description.trim();
    fields.description = customDescription ? adfDoc(customDescription, mentions) : null;
  }
  if (summary) fields.summary = summary;
  if (description) fields.description = adfDoc(description, mentions);
  if (input?.clearDescription === true) fields.description = null;
  if (issueType) fields.issuetype = { name: issueType };
  if (assigneeAccountId) fields.assignee = { accountId: assigneeAccountId };
  if (priorityName) fields.priority = { name: priorityName };
  if (Array.isArray(input?.labels)) fields.labels = labels;
  if (Array.isArray(input?.components)) fields.components = jiraComponentRefs(components);

  const fieldNames = Object.keys(fields).filter((key) => fields[key] !== undefined);
  if (!fieldNames.length) throw new Error("provide at least one issue field to update");

  const result = await jiraRestPut(`/rest/api/3/issue/${encodeURIComponent(issueKey)}`, { fields });
  return withMeta({
    ...restMeta(result),
    issueKey,
    issueUrl: new URL(`/browse/${issueKey}`, config.jiraBaseUrl).toString(),
    updatedFields: fieldNames.sort(),
    summary: summary || undefined,
    description: description || undefined,
    clearedDescription: input?.clearDescription === true || undefined,
    issueType: issueType || undefined,
    assigneeAccountId: assigneeAccountId || undefined,
    priorityName: priorityName || undefined,
    labels,
    components,
    mentions,
    bodyFormat: fields.description ? "jira_adf" : undefined,
  }, "jira", "write_result");
}

async function jiraRestGet(pathname, params = {}) {
  return withBrowserOperation(() => unqueuedJiraRestGet(pathname, params));
}

async function confluenceRestGet(pathname, params = {}) {
  return withBrowserOperation(() => unqueuedConfluenceRestGet(pathname, params));
}

async function jiraRestPost(pathname, body) {
  return jiraRestWrite("POST", pathname, body);
}

async function jiraRestPut(pathname, body) {
  return jiraRestWrite("PUT", pathname, body);
}

async function jiraRestWrite(method, pathname, body) {
  return withBrowserOperation(async () => {
    requireJiraBaseUrl();
    const p = await ensureHiddenPage();
    await p.goto(config.jiraBaseUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const url = new URL(String(pathname).replace(/^\/+/, ""), `${config.jiraBaseUrl}/`);
    assertAllowedProductUrl(url, config.jiraBaseUrl);
    if (!isAllowedJiraRestPath(url.pathname)) throw new Error(`Blocked non-Jira REST path: ${url.pathname}`);
    const payload = await jiraRestJsonRequest(p, url.toString(), method, body);
    const json = parseJsonText(payload.text);
    const loginRequired = looksLikeLoginOrAuth(payload.url || url.toString(), "", String(payload.text || ""));
    const errors = restErrors(payload.status, json, payload.text, loginRequired);
    return { ok: payload.status >= 200 && payload.status < 300 && errors.length === 0, status: payload.status, url: payload.url, json, text: payload.text, errors };
  });
}

async function jiraRestUploadAttachment(pathname, file) {
  return withBrowserOperation(async () => {
    requireJiraBaseUrl();
    const p = await ensureHiddenPage();
    await p.goto(config.jiraBaseUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const url = new URL(String(pathname).replace(/^\/+/, ""), `${config.jiraBaseUrl}/`);
    assertAllowedProductUrl(url, config.jiraBaseUrl);
    if (!isAllowedJiraRestPath(url.pathname)) throw new Error(`Blocked non-Jira REST path: ${url.pathname}`);
    const payload = await jiraRestMultipartRequest(p, url.toString(), file);
    const json = parseJsonText(payload.text);
    const loginRequired = looksLikeLoginOrAuth(payload.url || url.toString(), "", String(payload.text || ""));
    const errors = restErrors(payload.status, json, payload.text, loginRequired);
    return { ok: payload.status >= 200 && payload.status < 300 && errors.length === 0, status: payload.status, url: payload.url, json, text: payload.text, errors };
  });
}

async function jiraRestJsonRequest(p, targetUrl, requestMethod, requestBody) {
  await assertJiraPageReadyForWrite(p);
  return p.evaluate(async ({ targetUrl, requestMethod, requestBody, headers, timeoutMs }) => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetch(targetUrl, {
        method: requestMethod,
        credentials: "include",
        headers,
        body: JSON.stringify(requestBody ?? {}),
        signal: controller.signal,
      });
      const text = await response.text();
      return { status: response.status, url: response.url, text };
    } finally {
      clearTimeout(timer);
    }
  }, {
    targetUrl,
    requestMethod,
    requestBody: requestBody ?? {},
    headers: jiraRestMutationHeaders({ "Content-Type": "application/json" }),
    timeoutMs: 90_000,
  });
}

async function jiraRestMultipartRequest(p, targetUrl, file) {
  await assertJiraPageReadyForWrite(p);
  return p.evaluate(async ({ targetUrl, fileName, mimeType, contentBase64, headers, timeoutMs }) => {
    const binary = atob(contentBase64 || "");
    const bytes = new Uint8Array(binary.length);
    for (let index = 0; index < binary.length; index += 1) {
      bytes[index] = binary.charCodeAt(index);
    }
    const form = new FormData();
    form.append("file", new File([bytes], fileName, { type: mimeType || "application/octet-stream" }), fileName);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetch(targetUrl, {
        method: "POST",
        credentials: "include",
        headers,
        body: form,
        signal: controller.signal,
      });
      const text = await response.text();
      return { status: response.status, url: response.url, text };
    } finally {
      clearTimeout(timer);
    }
  }, {
    targetUrl,
    fileName: file.fileName,
    mimeType: file.mimeType || "application/octet-stream",
    contentBase64: String(file.contentBase64 || ""),
    headers: jiraRestMutationHeaders(),
    timeoutMs: 90_000,
  });
}

function jiraRestMutationHeaders(extra = {}) {
  return {
    Accept: "application/json",
    "X-Atlassian-Token": "no-check",
    "X-Requested-With": "XMLHttpRequest",
    ...extra,
  };
}

async function assertJiraPageReadyForWrite(p) {
  const jiraOrigin = new URL(config.jiraBaseUrl).origin;
  const currentURL = new URL(p.url());
  if (currentURL.origin !== jiraOrigin || looksLikeLoginOrAuth(currentURL.toString(), await safeTitle(p), "")) {
    throw new Error("Atlassian login is required.");
  }
}

async function jiraRestDownload(pathname, options = {}) {
  return withBrowserOperation(async () => {
    requireJiraBaseUrl();
    const p = await ensureHiddenPage();
    await p.goto(config.jiraBaseUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const url = new URL(String(pathname).replace(/^\/+/, ""), `${config.jiraBaseUrl}/`);
    assertAllowedProductUrl(url, config.jiraBaseUrl);
    if (!isAllowedJiraRestPath(url.pathname)) throw new Error(`Blocked non-Jira REST path: ${url.pathname}`);
    const maxBytes = clampInt(options.maxBytes, 64 * 1024, 1, 1024 * 1024);
    const maxDownloadBytes = clampInt(options.maxDownloadBytes, maxManagedDownloadBytes, 1, maxManagedDownloadBytes);
    const payload = await downloadAtlassianFile(p, url.toString(), { maxBytes, maxDownloadBytes });
    let saved;
    if (payload.status >= 200 && payload.status < 300 && !payload.downloadCapExceeded && options.saveFileName) {
      saved = await writeManagedDownload(payload.fullBytes, {
        namespace: options.downloadNamespace,
        fileName: options.saveFileName,
      });
    }
    const responseText = String(payload.text || "");
    const loginRequired = looksLikeLoginOrAuth(payload.url || url.toString(), "", responseText);
    const errors = restErrors(payload.status, undefined, responseText, loginRequired);
    if (payload.downloadCapExceeded) {
      errors.push(`Attachment exceeds managed download cap of ${maxDownloadBytes} bytes; local file was not saved.`);
    }
    const download = saved ? {
      local_path: saved.localPath,
      localPath: saved.localPath,
      local_relative_path: saved.localRelativePath,
      localRelativePath: saved.localRelativePath,
      directory: saved.downloadDirectory,
      file_name: saved.fileName,
      fileName: saved.fileName,
      sha256: saved.sha256,
      size_bytes: saved.sizeBytes,
      sizeBytes: saved.sizeBytes,
      source: "atlassian_managed_download",
    } : undefined;
    return {
      ok: payload.status >= 200 && payload.status < 300 && !payload.downloadCapExceeded,
      status: payload.status,
      url: payload.url,
      contentType: payload.contentType,
      bytesTotal: payload.bytesTotal,
      bytesReturned: payload.bytesReturned,
      truncated: payload.truncated,
      base64: payload.base64,
      text: payload.text,
      localPath: saved?.localPath,
      localRelativePath: saved?.localRelativePath,
      downloadDirectory: saved?.downloadDirectory,
      sha256: saved?.sha256,
      downloadMethod: payload.downloadMethod,
      downloadWarning: payload.downloadWarning,
      download,
      errors: [...new Set(errors)],
    };
  });
}

async function downloadAtlassianFile(p, targetUrl, options) {
  try {
    return await downloadAtlassianFileViaRequest(targetUrl, options);
  } catch (error) {
    const fallback = await downloadAtlassianFileViaPage(p, targetUrl, options);
    fallback.downloadMethod = "browser_fetch_fallback";
    fallback.downloadWarning = String(error?.message || error);
    return fallback;
  }
}

async function downloadAtlassianFileViaRequest(targetUrl, options) {
  if (!context?.request?.get) throw new Error("Browser request context is unavailable.");
  const response = await context.request.get(targetUrl, {
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
      downloadMethod: "browser_context_request",
      maxBytes: options.maxBytes,
      maxDownloadBytes: options.maxDownloadBytes,
    });
  }
  const body = await response.body();
  return downloadedPayload({
    status: response.status(),
    url: response.url(),
    contentType: headers["content-type"] || "",
    bytesTotal: body.length,
    fullBytes: body,
    downloadMethod: "browser_context_request",
    maxBytes: options.maxBytes,
    maxDownloadBytes: options.maxDownloadBytes,
  });
}

async function downloadAtlassianFileViaPage(p, targetUrl, options) {
  const payload = await p.evaluate(async ({ targetUrl, maxDownloadBytes }) => {
    const response = await fetch(targetUrl, {
      method: "GET",
      credentials: "include",
      headers: { Accept: "*/*" },
    });
    const chunks = [];
    let bytesTotal = 0;
    let downloadCapExceeded = false;
    if (response.body?.getReader) {
      const reader = response.body.getReader();
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        const chunk = value || new Uint8Array();
        if (bytesTotal + chunk.length > maxDownloadBytes) {
          const remaining = Math.max(0, maxDownloadBytes - bytesTotal);
          if (remaining > 0) chunks.push(chunk.subarray(0, remaining));
          bytesTotal += chunk.length;
          downloadCapExceeded = true;
          await reader.cancel().catch(() => {});
          break;
        }
        chunks.push(chunk);
        bytesTotal += chunk.length;
      }
    } else {
      const buffer = await response.arrayBuffer();
      const bytes = new Uint8Array(buffer);
      bytesTotal = bytes.length;
      downloadCapExceeded = bytes.length > maxDownloadBytes;
      chunks.push(bytes.subarray(0, Math.min(bytes.length, maxDownloadBytes)));
    }
    const limit = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
    const bytes = new Uint8Array(limit);
    let writeOffset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, writeOffset);
      writeOffset += chunk.length;
    }
    let binary = "";
    const chunkSize = 0x8000;
    for (let offset = 0; offset < limit; offset += chunkSize) {
      const chunk = bytes.subarray(offset, Math.min(offset + chunkSize, limit));
      binary += String.fromCharCode(...chunk);
    }
    return {
      status: response.status,
      url: response.url,
      contentType: response.headers.get("content-type") || "",
      bytesTotal,
      downloadCapExceeded,
      base64: btoa(binary),
    };
  }, { targetUrl, maxDownloadBytes: options.maxDownloadBytes });
  return downloadedPayload({
    status: payload.status,
    url: payload.url,
    contentType: payload.contentType,
    bytesTotal: payload.bytesTotal,
    fullBytes: Buffer.from(String(payload.base64 || ""), "base64"),
    downloadCapExceeded: payload.downloadCapExceeded,
    downloadMethod: "browser_fetch_fallback",
    maxBytes: options.maxBytes,
    maxDownloadBytes: options.maxDownloadBytes,
  });
}

function downloadedPayload(input) {
  const fullBytes = Buffer.isBuffer(input.fullBytes) ? input.fullBytes : Buffer.from(input.fullBytes || []);
  const downloadCapExceeded = Boolean(input.downloadCapExceeded) || fullBytes.length > input.maxDownloadBytes;
  const bytesReturned = Math.min(fullBytes.length, input.maxBytes);
  const returnedBytes = fullBytes.subarray(0, bytesReturned);
  return {
    status: input.status,
    url: input.url,
    contentType: input.contentType || "",
    bytesTotal: input.bytesTotal ?? fullBytes.length,
    bytesReturned,
    truncated: downloadCapExceeded || fullBytes.length > input.maxBytes,
    downloadCapExceeded,
    fullBytes,
    base64: returnedBytes.toString("base64"),
    text: returnedBytes.toString("utf8"),
    downloadMethod: input.downloadMethod,
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

function sanitizePathSegment(raw) {
  return String(raw || "")
    .replace(/[<>:"/\\|?*\u0000-\u001f]/g, "-")
    .replace(/\s+/g, " ")
    .replace(/^\.+$/, "")
    .trim()
    .slice(0, 120);
}

function sanitizeManagedDownloadFileName(raw) {
  return sanitizeAttachmentFileName(raw) || "download";
}

function parseContentLength(raw) {
  const parsed = Number.parseInt(String(raw || ""), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

async function unqueuedJiraRestGet(pathname, params = {}) {
  requireJiraBaseUrl();
  const p = await ensureHiddenPage();
  const url = new URL(String(pathname).replace(/^\/+/, ""), `${config.jiraBaseUrl}/`);
  assertAllowedProductUrl(url, config.jiraBaseUrl);
  if (!isAllowedJiraRestPath(url.pathname)) throw new Error(`Blocked non-Jira REST path: ${url.pathname}`);
  setSearchParams(url, params);
  const response = await p.goto(url.toString(), { waitUntil: "domcontentloaded" });
  await settle(p);
  const text = await safeBodyText(p);
  const json = parseJsonText(text);
  const title = await safeTitle(p);
  const loginRequired = looksLikeLoginOrAuth(p.url(), title || "", text);
  const errors = restErrors(response?.status() || null, json, text, loginRequired);
  return { ok: errors.length === 0, status: response?.status() || null, url: p.url(), json, text, errors };
}

async function unqueuedConfluenceRestGet(pathname, params = {}) {
  requireConfluenceBaseUrl();
  const p = await ensureHiddenPage();
  const url = confluenceRestUrl(pathname);
  assertAllowedProductUrl(url, config.confluenceBaseUrl);
  if (!isAllowedConfluenceRestPath(url.pathname, new URL(config.confluenceBaseUrl).pathname)) throw new Error(`Blocked non-Confluence REST path: ${url.pathname}`);
  setSearchParams(url, params);
  const response = await p.goto(url.toString(), { waitUntil: "domcontentloaded" });
  await settle(p);
  const text = await safeBodyText(p);
  const json = parseJsonText(text);
  const title = await safeTitle(p);
  const loginRequired = looksLikeLoginOrAuth(p.url(), title || "", text);
  const errors = restErrors(response?.status() || null, json, text, loginRequired);
  return { ok: errors.length === 0, status: response?.status() || null, url: p.url(), json, text, errors };
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
    const candidates = browserLaunchCandidates();
    let lastError;
    for (const candidate of candidates) {
      try {
        const browserType = browserTypeForCandidate(candidate);
        context = await browserType.launchPersistentContext(config.browserProfileDir, launchOptionsFor(candidate, nextMode));
        activeBrowser = publicBrowserCandidate(candidate);
        break;
      } catch (error) {
        lastError = error;
        if (candidate.required) {
          throw new Error(`Could not launch configured Atlassian browser ${candidate.name}: ${String(error?.message || error)}`);
        }
      }
    }
    if (!context) {
      throw new Error(`Could not launch an Atlassian browser: ${String(lastError?.message || lastError || "no browser candidates")}`);
    }
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

function launchOptionsFor(candidate, nextMode) {
  return {
    headless: nextMode === "hidden" ? config.browserHeadless : false,
    viewport: { width: 1440, height: 1000 },
    chromiumSandbox: true,
  };
}

function browserTypeForCandidate(candidate) {
  return chromium;
}

function browserLaunchCandidates() {
  return [bundledBrowserCandidate("Bundled Chromium")];
}

function bundledBrowserCandidate(name, fallback = false) {
  return {
    provider: "bundled",
    source: fallback ? "fallback" : "configured",
    name,
    warning: fallback ? "Using bundled Chromium fallback." : undefined,
  };
}

function publicBrowserCandidate(candidate) {
  return {
    provider: candidate.provider,
    source: candidate.source,
    name: candidate.name,
    family: candidate.family,
    channel: candidate.channel,
    executablePath: candidate.executablePath,
    warning: candidate.warning,
  };
}

function startLoginWatcher() {
  stopLoginWatcher();
  const deadline = Date.now() + 15 * 60 * 1000;
  autoLoginStatus = { state: "watching", message: "Waiting for Atlassian SSO to complete in the visible browser.", startedAt: new Date().toISOString() };
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
    autoLoginStatus = { state: "expired", message: "Timed out waiting for Atlassian login. Start login again if needed.", updatedAt: new Date().toISOString() };
    stopLoginWatcher();
    return;
  }
  try {
    if (!looksAuthenticated(page, config.jiraBaseUrl)) {
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
  autoLoginStatus = { state: "switching", message: "Atlassian login detected. Closing visible browser, starting hidden automation, and checking Jira plus Confluence.", updatedAt: new Date().toISOString() };
  try {
    const visiblePage = page ? await pageSummary(page) : null;
    await closeBrowser();
    const p = await launch("hidden");
    await p.goto(config.jiraBaseUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const products = await checkConfiguredProducts(p);
    autoLoginStatus = { state: "hidden", message: "Atlassian login detected. Hidden browser automation is ready for Jira and Confluence.", visiblePage, browser: activeBrowser, hiddenBrowser: await pageSummary(p), products, updatedAt: new Date().toISOString() };
  } finally {
    loginSwitching = false;
  }
}

async function checkConfiguredProducts(p) {
  const products = {};
  if (config.jiraBaseUrl) products.jira = await checkProduct(p, "jira", config.jiraBaseUrl, true);
  if (config.confluenceBaseUrl) products.confluence = await checkProduct(p, "confluence", config.confluenceBaseUrl, false);
  productStatus = products;
  return products;
}

async function checkProduct(p, product, baseUrl, detectProfile) {
  try {
    const response = await p.goto(baseUrl, { waitUntil: "domcontentloaded" });
    await settle(p);
    const title = await safeTitle(p);
    const text = await safeBodyText(p);
    const loginRequired = looksLikeLoginOrAuth(p.url(), title || "", text);
    const profile = !loginRequired && detectProfile ? detectProfileFromText(text) : undefined;
    return {
      product,
      connected: !loginRequired,
      state: loginRequired ? "login_required" : "connected",
      status: response?.status() || null,
      url: p.url(),
      title,
      profile,
      checkedAt: new Date().toISOString(),
    };
  } catch (error) {
    return { product, connected: false, state: "error", error: String(error?.message || error), checkedAt: new Date().toISOString() };
  }
}

async function productStatusFromCurrentPage(p) {
  const currentUrl = p.url();
  const products = { ...productStatus };
  if (config.jiraBaseUrl && sameHost(currentUrl, config.jiraBaseUrl)) {
    products.jira = { ...(products.jira || {}), product: "jira", url: currentUrl, title: await safeTitle(p), state: looksAuthenticated(p, config.jiraBaseUrl) ? "connected" : products.jira?.state || "browser_open" };
  }
  if (config.confluenceBaseUrl && sameHost(currentUrl, config.confluenceBaseUrl)) {
    products.confluence = { ...(products.confluence || {}), product: "confluence", url: currentUrl, title: await safeTitle(p), state: looksAuthenticated(p, config.confluenceBaseUrl) ? "connected" : products.confluence?.state || "browser_open" };
  }
  productStatus = products;
  return products;
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
  const haystack = `${url}\n${title}\n${text.slice(0, 2000)}`.toLowerCase();
  return /\/login|id\.atlassian\.com|auth\.atlassian\.com|login\.microsoftonline\.com|single sign|sign in|log in|verify your identity/.test(haystack);
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
  const jiraBaseUrl = normalizeURL(raw.jiraBaseUrl || "", false);
  const confluenceBaseUrl = normalizeURL(raw.confluenceBaseUrl || (jiraBaseUrl ? `${jiraBaseUrl}/wiki` : ""), true);
  return {
    jiraBaseUrl,
    confluenceBaseUrl,
    defaultJiraProjectKey: String(raw.defaultJiraProjectKey || "").trim().toUpperCase(),
    allowedProjects: arrayOfStrings(raw.allowedProjects).map((v) => v.toUpperCase()),
    allowedAuthDomains: arrayOfStrings(raw.allowedAuthDomains).map((v) => v.toLowerCase()),
    browserProvider: normalizeBrowserProvider(raw.browserProvider),
    browserChannel: normalizeBrowserChannel(raw.browserChannel),
    browserExecutablePath: normalizeBrowserExecutablePath(raw.browserExecutablePath),
    browserProfileDir: path.resolve(String(raw.browserProfileDir || path.join(stateDir, "browser-profile"))),
    browserHeadless: raw.browserHeadless !== false,
  };
}

function publicConfig() {
  return { ...config, browserProfileDir: config.browserProfileDir };
}

function normalizeBrowserProvider(raw) {
  return "bundled";
}

function normalizeBrowserChannel(raw) {
  return "";
}

function normalizeBrowserExecutablePath(raw) {
  return "";
}

function normalizeURL(raw, confluence = false) {
  const value = String(raw || "").trim().replace(/\/+$/, "");
  if (!value) return "";
  const url = new URL(value.includes("://") ? value : `https://${value}`);
  if (!["http:", "https:"].includes(url.protocol)) throw new Error("Atlassian URLs must use http or https.");
  if (confluence && !url.pathname.replace(/\/+$/, "") && url.hostname.toLowerCase().endsWith(".atlassian.net")) {
    url.pathname = "/wiki";
  }
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/+$/, "");
}

function requireJiraBaseUrl() {
  if (!config.jiraBaseUrl) throw new Error("Configure Jira base URL before connecting Atlassian.");
}

function requireConfluenceBaseUrl() {
  if (!config.confluenceBaseUrl) throw new Error("Configure Confluence base URL before using Confluence tools.");
}

function assertAllowedProductUrl(target, baseUrl) {
  const base = new URL(baseUrl);
  if (target.hostname !== base.hostname || target.protocol !== base.protocol) throw new Error(`Blocked URL outside configured Atlassian site: ${target.toString()}`);
}

function isAllowedJiraRestPath(pathname) {
  return pathname.startsWith("/rest/api/3/") || pathname === "/rest/api/3/search/jql" || pathname.startsWith("/rest/agile/1.0/");
}

function isAllowedConfluenceRestPath(pathname, basePath) {
  const prefix = String(basePath || "").replace(/\/+$/, "");
  const relative = prefix && pathname.startsWith(prefix) ? pathname.slice(prefix.length) || "/" : pathname;
  return relative.startsWith("/api/v2/") || relative === "/api/v2/spaces" || relative.startsWith("/rest/api/");
}

function confluenceRestUrl(pathname) {
  const base = config.confluenceBaseUrl.endsWith("/") ? config.confluenceBaseUrl : `${config.confluenceBaseUrl}/`;
  return new URL(String(pathname).replace(/^\/+/, ""), base);
}

function confluenceWebUrl(target) {
  const raw = String(target || "").trim();
  if (!raw) return undefined;

  const base = new URL(config.confluenceBaseUrl);
  const basePath = base.pathname.replace(/\/+$/, "");
  const baseWithSlash = config.confluenceBaseUrl.endsWith("/") ? config.confluenceBaseUrl : `${config.confluenceBaseUrl}/`;
  const isAbsolute = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw);
  const url = isAbsolute
    ? new URL(raw)
    : raw.startsWith("/")
      ? new URL(raw, base.origin)
      : new URL(raw.replace(/^\/+/, ""), baseWithSlash);

  if (url.protocol === base.protocol && url.hostname === base.hostname && basePath && url.pathname !== basePath && !url.pathname.startsWith(`${basePath}/`)) {
    url.pathname = `${basePath}/${url.pathname.replace(/^\/+/, "")}`;
  }

  return url.toString();
}

function setSearchParams(url, params) {
  for (const [key, value] of Object.entries(params || {})) {
    if (value !== undefined && value !== null && String(value) !== "") url.searchParams.set(key, String(value));
  }
}

function restErrors(status, json, text, loginRequired) {
  const errors = [];
  if (loginRequired) errors.push("Atlassian login is required.");
  if (status && status >= 400) errors.push(`HTTP ${status}`);
  const obj = asObject(json);
  if (typeof obj.errorMessages?.[0] === "string") errors.push(...obj.errorMessages);
  if (typeof obj.message === "string" && status && status >= 400) errors.push(obj.message);
  if (!json && status && status >= 400 && text) errors.push(truncateText(text, 500));
  return [...new Set(errors)];
}

function restMeta(result) {
  return { ok: result.ok, status: result.status, url: result.url, errors: result.errors || [] };
}

function withMeta(result, product, widget) {
  return {
    ...result,
    result_metadata: {
      integration: "atlassian",
      product,
      widget,
    },
  };
}

function assertAllowedIssueKey(issueKey) {
  const normalized = issueKey.trim().toUpperCase();
  const match = normalized.match(/^([A-Z][A-Z0-9]+)-\d+$/);
  if (!match) throw new Error(`Invalid Jira issue key: ${issueKey}`);
  assertAllowedProjectKey(match[1]);
  return normalized;
}

function assertAttachmentId(attachmentId) {
  const normalized = String(attachmentId || "").trim();
  if (!/^\d+$/.test(normalized)) throw new Error(`Invalid Jira attachment ID: ${attachmentId}`);
  return normalized;
}

function assertAllowedProjectKey(projectKey) {
  const normalized = String(projectKey || "").trim().toUpperCase();
  if (!/^[A-Z][A-Z0-9]+$/.test(normalized)) throw new Error(`Invalid Jira project key: ${projectKey}`);
  if (config.allowedProjects.length && !config.allowedProjects.includes(normalized)) {
    throw new Error(`Project ${normalized} is not allowed. Allowed projects: ${config.allowedProjects.join(", ")}`);
  }
  return normalized;
}

function assertAllowedJql(jql) {
  const trimmed = jql.trim();
  if (!trimmed) throw new Error("JQL cannot be empty.");
  if (!config.allowedProjects.length) return trimmed;
  const keys = new Set();
  for (const match of trimmed.matchAll(/\bproject\s*=\s*"?([A-Z][A-Z0-9]+)"?/gi)) keys.add(match[1].toUpperCase());
  for (const match of trimmed.matchAll(/\bproject\s+in\s*\(([^)]+)\)/gi)) {
    for (const raw of match[1].split(",")) {
      const key = raw.trim().replace(/^"|"$/g, "").toUpperCase();
      if (/^[A-Z][A-Z0-9]+$/.test(key)) keys.add(key);
    }
  }
  if (!keys.size) throw new Error(`JQL must include an allowed project clause. Allowed projects: ${config.allowedProjects.join(", ")}`);
  for (const key of keys) assertAllowedProjectKey(key);
  return trimmed;
}

function summarizeJiraIssue(issue) {
  const fields = asObject(issue.fields);
  const commentTotal = asObject(fields.comment).total;
  return compactObject({
    id: issue.id,
    key: issue.key,
    url: issue.key && config.jiraBaseUrl ? new URL(`/browse/${issue.key}`, config.jiraBaseUrl).toString() : undefined,
    summary: fields.summary,
    status: asObject(fields.status).name,
    issueType: asObject(fields.issuetype).name,
    priority: asObject(fields.priority).name,
    assignee: summarizeUser(fields.assignee),
    reporter: summarizeUser(fields.reporter),
    parent: fields.parent ? summarizeJiraIssue(asObject(fields.parent)) : undefined,
    labels: Array.isArray(fields.labels) ? fields.labels : undefined,
    components: arrayOfObjects(fields.components).map((item) => item.name).filter(Boolean),
    attachments: arrayOfObjects(fields.attachment).map(summarizeJiraAttachment),
    linkedIssues: summarizeJiraIssueLinks(fields.issuelinks),
    commentsCount: Number.isFinite(commentTotal) ? commentTotal : undefined,
    created: fields.created,
    updated: fields.updated,
    sprints: summarizeSprintField(fields.customfield_10020),
    // ADF document for rich rendering (size-capped) + plain-text preview;
    // the raw REST payload itself is never forwarded (rawIncluded: false).
    description: safeAdfDocument(fields.description),
    descriptionText: truncateText(adfToPlainText(asObject(fields.description)), 2000) || undefined,
  });
}

// Pass the ADF description through for rich client-side rendering, but only
// when it is a real ADF document of bounded size — anything else falls back
// to the plain-text preview.
const MAX_ADF_DESCRIPTION_CHARS = 20000;

function safeAdfDocument(value) {
  const doc = asObject(value);
  if (doc.type !== "doc" || !Array.isArray(doc.content)) return undefined;
  try {
    if (JSON.stringify(doc).length > MAX_ADF_DESCRIPTION_CHARS) return undefined;
  } catch {
    return undefined;
  }
  return doc;
}

function summarizeLinkedJiraIssue(issue) {
  const fields = asObject(issue.fields);
  return compactObject({
    id: issue.id,
    key: issue.key,
    url: issue.key && config.jiraBaseUrl ? new URL(`/browse/${issue.key}`, config.jiraBaseUrl).toString() : undefined,
    summary: fields.summary,
    status: asObject(fields.status).name,
    issueType: asObject(fields.issuetype).name,
  });
}

function summarizeJiraIssueLinks(value) {
  return arrayOfObjects(value).map((link) => {
    const type = asObject(link.type);
    const inward = asObject(link.inwardIssue);
    const outward = asObject(link.outwardIssue);
    return compactObject({
      id: link.id,
      typeName: type.name,
      inwardLabel: type.inward,
      outwardLabel: type.outward,
      inwardIssue: Object.keys(inward).length ? summarizeLinkedJiraIssue(inward) : undefined,
      outwardIssue: Object.keys(outward).length ? summarizeLinkedJiraIssue(outward) : undefined,
    });
  });
}

function summarizeJiraAttachment(attachment) {
  const obj = asObject(attachment);
  const id = String(obj.id || "").trim();
  return compactObject({
    id,
    fileName: obj.filename || obj.fileName,
    author: summarizeUser(obj.author),
    created: obj.created,
    sizeBytes: obj.size,
    mimeType: obj.mimeType,
    contentUrl: obj.content,
    thumbnailUrl: obj.thumbnail,
  });
}

function summarizeJiraComment(issueKey, comment) {
  const id = String(comment.id || "").trim();
  const body = asObject(comment.body);
  const properties = arrayOfObjects(comment.properties);
  const replyProperty = properties.find((property) => property.key === "tributary.replyTo");
  const parentCommentId = String(comment.parentId || asObject(replyProperty?.value).parentCommentId || "").trim();
  return compactObject({
    id,
    issueKey,
    url: id ? commentUrl(issueKey, id) : undefined,
    parentCommentId: parentCommentId || undefined,
    parentUrl: parentCommentId ? commentUrl(issueKey, parentCommentId) : undefined,
    author: summarizeUser(comment.author),
    updateAuthor: summarizeUser(comment.updateAuthor),
    created: comment.created,
    updated: comment.updated,
    bodyText: truncateText(adfToPlainText(body), 2000),
    renderedText: truncateText(stripHtml(String(comment.renderedBody || "")), 2000),
  });
}

function commentUrl(issueKey, commentId) {
  return new URL(`/browse/${issueKey}?focusedCommentId=${encodeURIComponent(commentId)}#comment-${encodeURIComponent(commentId)}`, config.jiraBaseUrl).toString();
}

function summarizeUser(user) {
  const obj = asObject(user);
  if (!Object.keys(obj).length) return undefined;
  return compactObject({
    displayName: obj.displayName,
    emailAddress: obj.emailAddress,
    accountId: obj.accountId,
    active: obj.active,
    accountType: obj.accountType,
  });
}

function summarizeJiraChangelog(issue) {
  const histories = arrayOfObjects(asObject(issue.changelog).histories);
  return histories.slice(0, 25).map((history) => ({
    created: history.created,
    author: summarizeUser(history.author),
    items: arrayOfObjects(history.items).map((item) => compactObject({ field: item.field, fromString: item.fromString, toString: item.toString })),
  }));
}

function summarizeSprintTransitions(issue) {
  return summarizeJiraChangelog(issue)
    .flatMap((history) => history.items.map((item) => ({ history, item })))
    .filter(({ item }) => /sprint/i.test(String(item.field || "")))
    .map(({ history, item }) => ({
      changedAt: history.created,
      fromSprintId: firstSprintId(item.fromString),
      toSprintId: firstSprintId(item.toString),
      fromSprintName: firstSprintName(item.fromString),
      toSprintName: firstSprintName(item.toString),
    }));
}

function summarizeSprintField(value) {
  if (!Array.isArray(value)) return undefined;
  return value.map((item) => typeof item === "string" ? { id: firstSprintId(item), name: firstSprintName(item), raw: item } : item);
}

function firstSprintId(text) {
  const match = String(text || "").match(/\bid=(\d+)/);
  return match?.[1];
}

function firstSprintName(text) {
  const match = String(text || "").match(/\bname=([^,\]]+)/);
  return match?.[1];
}

function summarizeJiraProject(project) {
  return compactObject({ id: project.id, key: project.key, name: project.name, projectTypeKey: project.projectTypeKey, simplified: project.simplified });
}

function summarizeJiraBoard(board) {
  const location = asObject(board.location);
  return compactObject({
    id: board.id,
    name: board.name,
    type: board.type,
    self: board.self,
    projectKey: location.projectKey,
    projectId: location.projectId,
    projectName: location.projectName,
    location,
  });
}

function summarizeConfluenceSearchResult(item) {
  const content = asObject(item.content);
  const resultGlobal = asObject(item.resultGlobalContainer);
  return compactObject({
    id: content.id || item.id,
    title: content.title || item.title,
    type: content.type || item.type,
    url: item.url ? confluenceWebUrl(item.url) : undefined,
    space: resultGlobal.title || resultGlobal.displayUrl,
    excerpt: stripHtml(String(item.excerpt || "")),
    lastModified: item.lastModified,
  });
}

function summarizeConfluenceSpace(space) {
  return compactObject({
    id: space.id,
    key: space.key,
    name: space.name,
    type: space.type,
    status: space.status,
    homepageId: space.homepageId,
    description: asObject(space.description).plain?.value || asObject(space.description).view?.value,
  });
}

function summarizeConfluencePage(page) {
  const body = asObject(page.body);
  const storage = asObject(body.storage).value || asObject(body.atlas_doc_format).value;
  return compactObject({
    id: page.id,
    title: page.title,
    status: page.status,
    spaceId: page.spaceId,
    parentId: page.parentId,
    authorId: page.authorId,
    createdAt: page.createdAt,
    version: asObject(page.version).number,
    url: page._links?.webui ? confluenceWebUrl(page._links.webui) : page.id ? confluenceWebUrl(`/pages/${page.id}`) : undefined,
    text: storage ? truncateText(stripHtml(String(storage)), 6000) : undefined,
  });
}

function summarizeConfluenceJson(json) {
  const obj = asObject(json);
  if (Array.isArray(obj.results)) return { resultCount: obj.results.length, keys: Object.keys(obj).slice(0, 20) };
  return { keys: Object.keys(obj).slice(0, 30) };
}

function adfDoc(text, mentions = []) {
  const content = markdownBlocksToAdf(String(text || "").trim(), normalizeMentionRefs(mentions));
  return {
    type: "doc",
    version: 1,
    content: content.length ? content : [paragraphNode("")],
  };
}

function markdownBlocksToAdf(markdown, mentions = []) {
  const lines = String(markdown || "").replace(/\r\n?/g, "\n").split("\n");
  const blocks = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i += 1;
      continue;
    }

    const fence = line.match(/^\s*```([\w.+-]*)\s*$/);
    if (fence) {
      const code = [];
      i += 1;
      while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) {
        code.push(lines[i]);
        i += 1;
      }
      if (i < lines.length) i += 1;
      blocks.push(codeBlockNode(code.join("\n"), fence[1]));
      continue;
    }

    if (isTableStart(lines, i)) {
      const tableLines = [lines[i]];
      i += 2;
      while (i < lines.length && lineLooksLikeTable(lines[i])) {
        tableLines.push(lines[i]);
        i += 1;
      }
      blocks.push(tableNode(tableLines, mentions));
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      blocks.push(headingNode(heading[2], heading[1].length, mentions));
      i += 1;
      continue;
    }

    if (/^\s*[-*_]{3,}\s*$/.test(line)) {
      blocks.push({ type: "rule" });
      i += 1;
      continue;
    }

    if (/^\s*>\s?/.test(line)) {
      const quoteLines = [];
      while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
        quoteLines.push(lines[i].replace(/^\s*>\s?/, ""));
        i += 1;
      }
      blocks.push({ type: "blockquote", content: [paragraphNode(quoteLines.join("\n"), mentions)] });
      continue;
    }

    if (/^\s*[-*+]\s+/.test(line) || /^\s*\d+[.)]\s+/.test(line)) {
      const ordered = /^\s*\d+[.)]\s+/.test(line);
      const items = [];
      const itemPattern = ordered ? /^\s*\d+[.)]\s+(.+)$/ : /^\s*[-*+]\s+(.+)$/;
      while (i < lines.length && itemPattern.test(lines[i])) {
        const itemText = lines[i].replace(itemPattern, "$1").trim();
        items.push({ type: "listItem", content: [paragraphNode(itemText, mentions)] });
        i += 1;
      }
      blocks.push(ordered ? { type: "orderedList", attrs: { order: 1 }, content: items } : { type: "bulletList", content: items });
      continue;
    }

    const paragraph = [line];
    i += 1;
    while (i < lines.length && lines[i].trim() && !startsSpecialBlock(lines, i)) {
      paragraph.push(lines[i]);
      i += 1;
    }
    blocks.push(paragraphNode(paragraph.join("\n"), mentions));
  }
  return blocks;
}

function startsSpecialBlock(lines, index) {
  const line = lines[index] || "";
  return /^\s*```/.test(line) ||
    isTableStart(lines, index) ||
    /^(#{1,6})\s+/.test(line) ||
    /^\s*[-*_]{3,}\s*$/.test(line) ||
    /^\s*>\s?/.test(line) ||
    /^\s*[-*+]\s+/.test(line) ||
    /^\s*\d+[.)]\s+/.test(line);
}

function paragraphNode(text, mentions = []) {
  const content = [];
  const lines = String(text || "").split("\n");
  lines.forEach((line, index) => {
    if (index > 0) content.push({ type: "hardBreak" });
    content.push(...inlineNodes(line, mentions));
  });
  return { type: "paragraph", content };
}

function headingNode(text, level, mentions = []) {
  return {
    type: "heading",
    attrs: { level: Math.max(1, Math.min(6, Number(level) || 1)) },
    content: inlineNodes(text, mentions),
  };
}

function codeBlockNode(code, language) {
  const lang = String(language || "").trim().slice(0, 32);
  const node = {
    type: "codeBlock",
    content: [{ type: "text", text: String(code || "") }],
  };
  if (lang) node.attrs = { language: lang };
  return node;
}

function isTableStart(lines, index) {
  return lineLooksLikeTable(lines[index]) && isTableSeparator(lines[index + 1]);
}

function lineLooksLikeTable(line) {
  return typeof line === "string" && line.includes("|") && line.replace(/\|/g, "").trim().length > 0;
}

function isTableSeparator(line) {
  if (!lineLooksLikeTable(line)) return false;
  return splitTableRow(line).every((cell) => /^:?-{3,}:?$/.test(cell.trim()));
}

function splitTableRow(line) {
  return String(line || "")
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

function tableNode(rows, mentions = []) {
  const header = splitTableRow(rows[0]);
  const bodyRows = rows.slice(1).map(splitTableRow);
  const width = Math.max(header.length, ...bodyRows.map((row) => row.length), 1);
  const pad = (row) => Array.from({ length: width }, (_, index) => row[index] || "");
  return {
    type: "table",
    attrs: { isNumberColumnEnabled: false, layout: "default" },
    content: [
      {
        type: "tableRow",
        content: pad(header).map((cell) => tableCellNode(cell, true, mentions)),
      },
      ...bodyRows.map((row) => ({
        type: "tableRow",
        content: pad(row).map((cell) => tableCellNode(cell, false, mentions)),
      })),
    ],
  };
}

function tableCellNode(text, header, mentions = []) {
  return {
    type: header ? "tableHeader" : "tableCell",
    content: [paragraphNode(text, mentions)],
  };
}

function inlineNodes(text, mentions = []) {
  const source = String(text || "");
  const nodes = [];
  const tokenPattern = /(\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)|`([^`]+)`|\*\*([^*]+)\*\*|__([^_]+)__|\*([^*\n]+)\*|_([^_\n]+)_)/g;
  let last = 0;
  let match;
  while ((match = tokenPattern.exec(source)) !== null) {
    if (match.index > last) nodes.push(...plainInlineNodes(source.slice(last, match.index), mentions));
    if (match[2] && match[3]) {
      nodes.push(textNode(match[2], [{ type: "link", attrs: { href: match[3] } }]));
    } else if (match[4]) {
      nodes.push(textNode(match[4], [{ type: "code" }]));
    } else if (match[5] || match[6]) {
      nodes.push(textNode(match[5] || match[6], [{ type: "strong" }]));
    } else if (match[7] || match[8]) {
      nodes.push(textNode(match[7] || match[8], [{ type: "em" }]));
    }
    last = match.index + match[0].length;
  }
  if (last < source.length) nodes.push(...plainInlineNodes(source.slice(last), mentions));
  return nodes.filter(Boolean);
}

function plainInlineNodes(text, mentions = []) {
  const source = String(text || "");
  if (!source) return [];
  const refs = normalizeMentionRefs(mentions);
  if (!refs.length) return [textNode(source)];
  const nodes = [];
  let cursor = 0;
  const lower = source.toLowerCase();
  while (cursor < source.length) {
    let best;
    for (const ref of refs) {
      for (const token of ref.tokens) {
        const index = lower.indexOf(token.lower, cursor);
        if (index < 0) continue;
        if (!best || index < best.index || (index === best.index && token.text.length > best.token.text.length)) {
          best = { index, ref, token };
        }
      }
    }
    if (!best) {
      nodes.push(textNode(source.slice(cursor)));
      break;
    }
    if (best.index > cursor) nodes.push(textNode(source.slice(cursor, best.index)));
    nodes.push(mentionNode(best.ref, source.slice(best.index, best.index + best.token.text.length)));
    cursor = best.index + best.token.text.length;
  }
  return nodes.filter(Boolean);
}

function normalizeMentionRefs(raw) {
  const items = Array.isArray(raw) ? raw : [];
  const out = [];
  const seen = new Set();
  for (const item of items) {
    const obj = asObject(item);
    const accountId = String(obj.accountId || obj.account_id || obj.id || "").trim();
    const displayName = String(obj.displayName || obj.display_name || obj.name || "").trim().replace(/^@+/, "");
    let mentionText = String(obj.mentionText || obj.mention_text || obj.text || "").trim();
    if (mentionText && !mentionText.startsWith("@") && !mentionText.startsWith("<@")) mentionText = `@${mentionText.replace(/^@+/, "")}`;
    if (!accountId || !displayName) continue;
    const key = accountId.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    const tokenTexts = [mentionText, `@${displayName}`, `<@${accountId}>`].filter(Boolean);
    out.push({
      accountId,
      displayName,
      mentionText: mentionText || undefined,
      tokens: [...new Set(tokenTexts)].map((token) => ({ text: token, lower: token.toLowerCase() })),
    });
  }
  return out;
}

function mentionNode(ref, typedText) {
  return {
    type: "mention",
    attrs: {
      id: ref.accountId,
      text: typedText && typedText.startsWith("@") ? typedText : `@${ref.displayName}`,
    },
  };
}

function textNode(text, marks = undefined) {
  const value = String(text || "");
  if (!value) return undefined;
  return marks?.length ? { type: "text", text: value, marks } : { type: "text", text: value };
}

function replyReferenceBlock(issueKey, commentId, label) {
  const prefix = String(label || "").trim() || "Replying to Jira comment";
  return {
    type: "paragraph",
    content: [
      { type: "text", text: `${prefix} ` },
      { type: "text", text: `#${commentId}`, marks: [{ type: "link", attrs: { href: commentUrl(issueKey, commentId) } }] },
      { type: "text", text: ":" },
    ],
  };
}

function adfToPlainText(node) {
  if (!node || typeof node !== "object") return "";
  if (node.type === "text") return String(node.text || "");
  if (node.type === "hardBreak") return "\n";
  const children = Array.isArray(node.content) ? node.content : [];
  if (node.type === "table") return children.map(adfToPlainText).join("\n");
  if (node.type === "tableRow") return children.map(adfToPlainText).join(" | ");
  if (node.type === "paragraph" || node.type === "heading" || node.type === "codeBlock") return children.map(adfToPlainText).join("");
  return children.map(adfToPlainText).join("\n");
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
  throw new Error(`Playwright package is required for Atlassian browser automation. Install dependencies with npm install. ${failures.join(" | ")}`);
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

function arrayOfObjects(value) {
  return Array.isArray(value) ? value.filter((item) => item && typeof item === "object" && !Array.isArray(item)) : [];
}

function arrayOfStrings(value) {
  return Array.isArray(value) ? value.map((item) => String(item || "").trim()).filter(Boolean) : [];
}

function dedupeStrings(values) {
  const seen = new Set();
  const out = [];
  for (const value of values) {
    const key = String(value || "").trim().toLowerCase();
    if (!key || seen.has(key)) continue;
    seen.add(key);
    out.push(String(value).trim());
  }
  return out;
}

function jiraComponentRefs(componentNames) {
  return dedupeStrings(componentNames).map((name) => ({ name }));
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

function sanitizeAttachmentFileName(raw) {
  const value = String(raw || "").replace(/\\/g, "/").split("/").pop() || "";
  return value.replace(/\0/g, "").trim().replace(/^\.+$/, "").slice(0, 180);
}

function isTextLikeContent(fileName, mimeType) {
  const mime = String(mimeType || "").toLowerCase();
  if (mime.startsWith("text/") || /json|xml|csv|yaml|markdown|javascript|typescript|sql/.test(mime)) return true;
  return /\.(txt|md|markdown|json|csv|tsv|xml|yaml|yml|log|sql|js|jsx|ts|tsx|go|py|java|rb|rs|c|cc|cpp|h|hpp|css|html)$/i.test(String(fileName || ""));
}

function compactObject(obj) {
  return Object.fromEntries(Object.entries(obj).filter(([, value]) => value !== undefined && value !== null && value !== "" && (!Array.isArray(value) || value.length > 0)));
}

function stripHtml(html) {
  return String(html || "").replace(/<[^>]*>/g, " ").replace(/\s+/g, " ").trim();
}

function jqlValue(value) {
  return `"${String(value).replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function cqlEscape(value) {
  return String(value).replace(/\\/g, "\\\\").replace(/"/g, '\\"');
}

function sameHost(a, b) {
  try { return new URL(a).hostname === new URL(b).hostname; } catch { return false; }
}

function extractConfluencePageId(rawUrl) {
  const text = String(rawUrl || "");
  const patterns = [/\/pages\/(\d+)/, /[?&]pageId=(\d+)/, /\/wiki\/spaces\/[^/]+\/pages\/(\d+)/];
  for (const pattern of patterns) {
    const match = text.match(pattern);
    if (match) return match[1];
  }
  return "";
}

function detectProfileFromText(text) {
  const email = String(text || "").match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/i)?.[0];
  return email ? { emailAddress: email } : undefined;
}

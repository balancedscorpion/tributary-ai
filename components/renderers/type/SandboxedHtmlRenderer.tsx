import React, { useEffect, useMemo, useRef, useState } from "react";
import { AlertTriangle, Code2 } from "lucide-react";
import { registerCustomResolver } from "../registry";
import type { RendererEntry, RendererProps } from "../types";
import {
  getSandboxedHtmlArtifactMetadata,
  isSandboxedHtmlArtifact,
  SANDBOXED_HTML_SANDBOX_POLICY,
} from "@/types";
import type { SandboxedHtmlArtifactMetadata } from "@/types";
import { cn } from "@/lib/utils";

const SANDBOX_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline'",
  "style-src 'unsafe-inline'",
  "img-src data: blob:",
  "font-src data:",
  "media-src data: blob:",
  "connect-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-src 'none'",
  "worker-src 'none'",
].join("; ");

function scriptJSON(value: unknown): string {
  const json = JSON.stringify(value ?? null) || "null";
  return json.replace(/</g, "\\u003c");
}

function getDataBridgeMetadata(artifact: RendererProps["artifact"]) {
  const metadata = (getSandboxedHtmlArtifactMetadata(artifact) ||
    {}) as Partial<SandboxedHtmlArtifactMetadata>;
  const dataAccess =
    metadata.data_access && typeof metadata.data_access === "object"
      ? (metadata.data_access as Record<string, unknown>)
      : {};
  const columns = Array.isArray(metadata.data_source_columns)
    ? metadata.data_source_columns
        .map((value) => String(value || "").trim())
        .filter(Boolean)
    : [];
  const rowCount = Number(metadata.data_source_row_count);
  const maxPageSize = Number(dataAccess.max_page_size);

  return {
    artifactId: String(metadata.data_source_artifact_id || "").trim(),
    title: String(metadata.data_source_title || "").trim(),
    columns,
    rowCount: Number.isFinite(rowCount) && rowCount >= 0 ? rowCount : null,
    maxPageSize:
      Number.isFinite(maxPageSize) && maxPageSize > 0
        ? Math.min(500, Math.trunc(maxPageSize))
        : 100,
  };
}

function runtimeErrorScript(
  artifactId: string,
  dataBridgeMetadata: ReturnType<typeof getDataBridgeMetadata>,
): string {
  return `<script>
(function () {
  var artifactId = ${scriptJSON(artifactId)};
  var dataBridgeMetadata = ${scriptJSON(dataBridgeMetadata)};
  var requestSeq = 0;
  var pending = {};
  function notify(kind, message, source, line, column, stack) {
    try {
      parent.postMessage({
        source: "db-streaming-html-sandbox",
        artifactId: artifactId,
        kind: kind,
        message: String(message || "Unknown sandbox error"),
        filename: source || "",
        line: line || 0,
        column: column || 0,
        stack: stack || ""
      }, "*");
    } catch (_) {}
  }
  window.addEventListener("error", function (event) {
    notify("error", event.message, event.filename, event.lineno, event.colno, event.error && event.error.stack);
  });
  window.addEventListener("unhandledrejection", function (event) {
    var reason = event.reason;
    notify("unhandledrejection", reason && (reason.message || reason.toString && reason.toString()), "", 0, 0, reason && reason.stack);
  });
  window.sandboxData = {
    getMetadata: function () {
      return JSON.parse(JSON.stringify(dataBridgeMetadata));
    },
    getRows: function (request) {
      requestSeq += 1;
      var requestId = "rows:" + requestSeq;
      return new Promise(function (resolve, reject) {
        pending[requestId] = { resolve: resolve, reject: reject };
        try {
          parent.postMessage({
            source: "db-streaming-html-sandbox",
            artifactId: artifactId,
            kind: "data_request",
            requestId: requestId,
            request: request || {}
          }, "*");
        } catch (error) {
          delete pending[requestId];
          reject(error);
        }
      });
    }
  };
  window.addEventListener("message", function (event) {
    var data = event.data;
    if (!data || typeof data !== "object") return;
    if (data.source !== "db-streaming-html-sandbox-parent") return;
    if (data.artifactId !== artifactId) return;
    if (data.kind !== "data_response") return;
    var entry = pending[data.requestId];
    if (!entry) return;
    delete pending[data.requestId];
    if (data.ok) {
      entry.resolve(data.payload);
    } else {
      entry.reject(new Error(String(data.error || "Data request failed")));
    }
  });
})();
</script>`;
}

function injectSandboxHead(
  html: string,
  artifactId: string,
  dataBridgeMetadata: ReturnType<typeof getDataBridgeMetadata>,
): string {
  const csp = `<meta http-equiv="Content-Security-Policy" content="${SANDBOX_CSP}">`;
  const boot = runtimeErrorScript(artifactId, dataBridgeMetadata);
  const headContent = `${csp}${boot}`;
  const trimmed = html.trim();

  if (/<head(\s[^>]*)?>/i.test(trimmed)) {
    return trimmed.replace(
      /<head(\s[^>]*)?>/i,
      (match) => `${match}${headContent}`,
    );
  }
  if (/<html(\s[^>]*)?>/i.test(trimmed)) {
    return trimmed.replace(
      /<html(\s[^>]*)?>/i,
      (match) => `${match}<head>${headContent}</head>`,
    );
  }
  const doctypeMatch = trimmed.match(/^\s*<!doctype html>\s*/i);
  if (doctypeMatch) {
    const withoutDoctype = trimmed.slice(doctypeMatch[0].length);
    return `<!doctype html><html><head>${headContent}</head><body>${withoutDoctype}</body></html>`;
  }
  return `<!doctype html><html><head>${headContent}</head><body>${trimmed}</body></html>`;
}

const SandboxedHtmlRenderer: React.FC<RendererProps> = ({
  artifact,
  className,
}) => {
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const [runtimeError, setRuntimeError] = useState<string | null>(null);
  const canRender = isSandboxedHtmlArtifact(artifact);
  const dataBridgeMetadata = useMemo(() => getDataBridgeMetadata(artifact), [
    artifact,
  ]);
  const srcDoc = useMemo(
    () =>
      canRender
        ? injectSandboxHead(
            artifact.content || "",
            artifact.id,
            dataBridgeMetadata,
          )
        : "",
    [artifact.content, artifact.id, canRender, dataBridgeMetadata],
  );

  useEffect(() => {
    setRuntimeError(null);
  }, [srcDoc]);

  useEffect(() => {
    if (!canRender) return;
    const postDataResponse = (
      requestId: string,
      response: { ok: true; payload: unknown } | { ok: false; error: string },
    ) => {
      iframeRef.current?.contentWindow?.postMessage(
        {
          source: "db-streaming-html-sandbox-parent",
          artifactId: artifact.id,
          kind: "data_response",
          requestId,
          ...response,
        },
        "*",
      );
    };

    const handleDataRequest = async (
      requestId: string,
      request: Record<string, unknown>,
    ) => {
      const sourceArtifactId = dataBridgeMetadata.artifactId;
      if (!sourceArtifactId) {
        postDataResponse(requestId, {
          ok: false,
          error: "No data source is bound to this artifact.",
        });
        return;
      }

      const rawOffset = Number(request.offset);
      const rawLimit = Number(request.limit);
      const offset =
        Number.isFinite(rawOffset) && rawOffset > 0 ? Math.trunc(rawOffset) : 0;
      const limit =
        Number.isFinite(rawLimit) && rawLimit > 0
          ? Math.min(dataBridgeMetadata.maxPageSize, Math.trunc(rawLimit))
          : dataBridgeMetadata.maxPageSize;
      const rawColumns = Array.isArray(request.columns) ? request.columns : [];
      const allowedColumns = new Set(dataBridgeMetadata.columns);
      const columns = rawColumns
        .map((value) => String(value || "").trim())
        .filter(Boolean)
        .filter(
          (value) => allowedColumns.size === 0 || allowedColumns.has(value),
        )
        .slice(0, 100);

      const params = new URLSearchParams();
      params.set("offset", String(offset));
      params.set("limit", String(limit));
      if (columns.length > 0) {
        params.set("columns", columns.join(","));
      }

      try {
        const res = await fetch(
          `/api/artifacts/${encodeURIComponent(sourceArtifactId)}/rows?${params.toString()}`,
          { method: "GET", headers: { Accept: "application/json" } },
        );
        const payload = await res.json().catch(() => null);
        if (!res.ok) {
          const message =
            payload && typeof payload.error === "string"
              ? payload.error
              : `HTTP ${res.status}`;
          postDataResponse(requestId, { ok: false, error: message });
          return;
        }
        postDataResponse(requestId, { ok: true, payload });
      } catch (error: any) {
        postDataResponse(requestId, {
          ok: false,
          error: String(error?.message || error || "Data request failed"),
        });
      }
    };

    const onMessage = (event: MessageEvent) => {
      if (event.source !== iframeRef.current?.contentWindow) return;
      const data = event.data;
      if (!data || typeof data !== "object") return;
      if ((data as any).source !== "db-streaming-html-sandbox") return;
      if ((data as any).artifactId !== artifact.id) return;
      if ((data as any).kind === "data_request") {
        const requestId = String((data as any).requestId || "").trim();
        if (!requestId) return;
        const request =
          (data as any).request && typeof (data as any).request === "object"
            ? ((data as any).request as Record<string, unknown>)
            : {};
        void handleDataRequest(requestId, request);
        return;
      }
      const message = String(
        (data as any).message || "Sandbox runtime error",
      ).trim();
      const line = Number((data as any).line);
      const column = Number((data as any).column);
      const suffix =
        Number.isFinite(line) && line > 0
          ? ` (${line}${Number.isFinite(column) && column > 0 ? `:${column}` : ""})`
          : "";
      setRuntimeError(`${message}${suffix}`);
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [artifact.id, canRender, dataBridgeMetadata]);

  if (!canRender) {
    return (
      <div
        className={cn(
          "flex h-full min-h-[240px] items-center justify-center rounded-lg border border-border bg-muted/20 px-3 text-xs text-muted-foreground",
          className,
        )}
        data-testid="sandboxed-html-renderer-unavailable"
      >
        HTML preview unavailable
      </div>
    );
  }

  return (
    <div
      className={cn(
        "relative h-full min-h-[240px] overflow-hidden rounded-lg border border-border bg-background",
        className,
      )}
      data-testid="sandboxed-html-renderer"
    >
      <iframe
        ref={iframeRef}
        title={artifact.title}
        srcDoc={srcDoc}
        sandbox={SANDBOXED_HTML_SANDBOX_POLICY}
        referrerPolicy="no-referrer"
        className="h-full w-full border-0 bg-white"
        data-testid="sandboxed-html-iframe"
      />
      {runtimeError && (
        <div className="absolute bottom-3 left-3 right-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive shadow-lg backdrop-blur">
          <div className="flex items-start gap-2">
            <AlertTriangle size={14} className="mt-0.5 shrink-0" />
            <div className="min-w-0">
              <div className="font-medium">Sandbox runtime error</div>
              <div className="break-words font-mono text-[11px]">
                {runtimeError}
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

const rendererEntry: RendererEntry = {
  component: SandboxedHtmlRenderer,
  label: "Interactive HTML",
  icon: Code2,
};

registerCustomResolver((artifact) =>
  isSandboxedHtmlArtifact(artifact) ? rendererEntry : null,
);

export default SandboxedHtmlRenderer;

/**
 * Shared frontend contract for tool error diagnostics.
 *
 * The tool / backend describes the failure by attaching structured diagnostic
 * metadata to the result envelope. The frontend reads it here and renders it
 * uniformly across every connector and the generic ErrorRenderer.
 *
 * Source of truth — in precedence order:
 *
 *   1. Nested diagnostic object on `result_metadata`:
 *
 *        result_metadata: {
 *          diagnostic: {
 *            title, code, kind, why, try_this, failed_request, message
 *          }
 *        }
 *
 *      (Aliases also accepted: `error_diagnostic`, `diagnostics`.)
 *
 *   2. Nested diagnostic object on `raw_result` (inline on the payload).
 *
 *   3. Direct fields on either `result_metadata` or `raw_result`
 *      (e.g. `result_metadata.why`, `raw_result.code`).
 *
 *   4. Connector-specific string classifier — opt-in fallback for legacy
 *      plain-error-string tools (GitHub uses this for 401 / 403 / 404).
 *
 *   5. Final generic fallback ("Request failed").
 *
 * Important: this metadata comes from the TOOL or the backend's generic
 * failed-tool wrapper, not from free-form LLM text. The LLM receives the same
 * enriched result envelope and is prompted to explain the likely cause, while
 * the UI uses the structured metadata for deterministic card/inspector copy.
 */

import React, { useState } from 'react';
import { AlertTriangle, Check, ChevronRight, Copy } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { Artifact } from '@/types';
import {
  extractToolErrorDiagnosticFromArtifact,
  type ConnectorClassifier,
  type ToolErrorDiagnostic,
} from '@/lib/toolErrorDiagnostics';
export type { ConnectorClassifier, ToolErrorDiagnostic } from '@/lib/toolErrorDiagnostics';

// ---------- extraction ----------

export function extractToolErrorDiagnostic(
  artifact: Artifact,
  opts?: { classifier?: ConnectorClassifier },
): ToolErrorDiagnostic {
  return extractToolErrorDiagnosticFromArtifact(artifact, opts);
}

// ---------- shared body component ----------

const wrapPre =
  'font-mono text-[12px] whitespace-pre-wrap [overflow-wrap:anywhere] bg-muted/40 border border-border rounded-md px-3 py-2.5 text-foreground';

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // ignore
    }
  };
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className="inline-flex items-center justify-center rounded p-0.5 text-muted-foreground/60 hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
    >
      {copied ? (
        <Check size={12} className="text-emerald-600" aria-hidden />
      ) : (
        <Copy size={12} aria-hidden />
      )}
    </button>
  );
}

function Disclosure({
  label,
  children,
  defaultOpen = false,
}: {
  label: string;
  children: React.ReactNode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
      >
        <ChevronRight
          size={12}
          aria-hidden
          className={cn('transition-transform', open && 'rotate-90')}
        />
        {label}
      </button>
      {open && <div className="mt-2">{children}</div>}
    </div>
  );
}

/**
 * Renders everything below the header band — diagnostic summary, failed
 * request, why / try-this, and the raw details disclosure. Both the GitHub
 * inspector's error branch and the generic ErrorRenderer use this so the two
 * surfaces share the same vocabulary.
 */
export function ErrorDiagnosticBody({
  diagnostic,
  rawJson,
  failedRequestFallback,
  toolArgs,
  className,
}: {
  diagnostic: ToolErrorDiagnostic;
  rawJson: string;
  /**
   * Default for the "Failed request" zone when the diagnostic itself doesn't
   * carry one — e.g. GitHub passes its synthesised "$ gh …" line; the generic
   * renderer passes a `tool_name(args)` fallback.
   */
  failedRequestFallback?: string | null;
  toolArgs?: Record<string, any> | null;
  className?: string;
}) {
  const failedRequest = diagnostic.failedRequest || failedRequestFallback || null;

  return (
    <div className={cn('flex flex-col gap-3 px-4 py-4', className)}>
      {/* Diagnostic summary — neutral surface, destructive accent on icon + code chip. */}
      <div role="alert" className="rounded-md border border-border bg-muted/40 p-3">
        <div className="flex items-start gap-2.5">
          <AlertTriangle
            size={16}
            className="mt-0.5 shrink-0 text-destructive"
            aria-hidden
          />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-[13px] font-medium text-foreground">
                {diagnostic.title}
              </h3>
              {diagnostic.code && (
                <span className="inline-flex items-center rounded-md border border-destructive/30 bg-destructive/10 px-1.5 py-0.5 font-mono text-[11px] font-medium text-destructive">
                  {diagnostic.code}
                </span>
              )}
            </div>
            <p className="mt-1.5 text-[13px] text-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
              {diagnostic.message}
            </p>
          </div>
          <div className="shrink-0">
            <CopyButton value={diagnostic.message} label="Copy error message" />
          </div>
        </div>
      </div>

      {failedRequest && (
        <div>
          <div className="mb-1.5 text-xs text-muted-foreground/60">Failed request</div>
          <div className="font-mono text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 px-3 py-2.5">
            {failedRequest}
          </div>
          {toolArgs && Object.keys(toolArgs).length > 0 && (
            <div className="mt-2">
              <Disclosure label="raw args">
                <pre className={wrapPre}>{JSON.stringify(toolArgs, null, 2)}</pre>
              </Disclosure>
            </div>
          )}
        </div>
      )}

      {(diagnostic.why || diagnostic.tryThis) && (
        <div className="flex flex-col gap-2.5 border-l-2 border-blue-500/40 pl-3">
          {diagnostic.why && (
            <div>
              <div className="text-[11px] uppercase tracking-wide text-muted-foreground/60">
                Why
              </div>
              <p className="mt-0.5 text-[13px] text-foreground [overflow-wrap:anywhere]">
                {diagnostic.why}
              </p>
            </div>
          )}
          {diagnostic.tryThis && (
            <div>
              <div className="text-[11px] uppercase tracking-wide text-muted-foreground/60">
                Try this
              </div>
              <p className="mt-0.5 text-[13px] text-foreground [overflow-wrap:anywhere]">
                {diagnostic.tryThis}
              </p>
            </div>
          )}
        </div>
      )}

      <Disclosure label="Details">
        <pre className={wrapPre}>{rawJson}</pre>
      </Disclosure>
    </div>
  );
}

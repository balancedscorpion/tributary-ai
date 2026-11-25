import type { Highlighter } from 'shiki';
import { createJavaScriptRegexEngine } from 'shiki/engine/javascript';
import { normalizeLanguageLabel } from './markdownLanguage';

let highlighterPromise: Promise<Highlighter> | null = null;
let shikiModulePromise: Promise<typeof import('shiki')> | null = null;

// Keep this list small and explicit for fast startup and predictable highlighting.
// csv is NOT a valid Shiki grammar - omit it to avoid breaking createHighlighter.
const PRELOADED_LANGS = [
  'json', 'sql', 'javascript', 'typescript', 'python',
  'go', 'html', 'css', 'yaml', 'bash', 'markdown', 'jsx', 'tsx',
] as const;

async function getShikiModule(): Promise<typeof import('shiki')> {
  if (!shikiModulePromise) {
    shikiModulePromise = import('shiki').catch((err) => {
      shikiModulePromise = null;
      throw err;
    });
  }
  return shikiModulePromise;
}

export function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    highlighterPromise = getShikiModule().then((shiki) =>
      shiki.createHighlighter({
        themes: ['github-dark-default'],
        langs: [...PRELOADED_LANGS],
        // Use the JavaScript regex engine to avoid WASM runtime failures that
        // can silently disable syntax highlighting in some environments.
        engine: createJavaScriptRegexEngine(),
      }),
    ).catch((err) => {
      // Reset so a retry is possible on next call
      highlighterPromise = null;
      throw err;
    });
  }
  return highlighterPromise;
}

function inferLanguageFromCode(code: string): string | null {
  const sample = String(code || '').slice(0, 8000);
  if (!sample.trim()) return null;

  // Prefer strict JSON inference first.
  const trimmed = sample.trim();
  if (
    (trimmed.startsWith('{') || trimmed.startsWith('['))
    && /"[A-Za-z0-9_.-]+"\s*:/.test(trimmed)
  ) {
    return 'json';
  }

  if (
    /\b(select|with|insert|update|delete|create|alter|drop)\b[\s\S]{0,80}\b(from|into|table)\b/i.test(sample)
    || /\bwhere\b[\s\S]{0,120}\b(group\s+by|order\s+by|limit)\b/i.test(sample)
  ) {
    return 'sql';
  }

  if (
    /\b(function|const|let|var|import|export|class)\b/.test(sample)
    || /=>/.test(sample)
  ) {
    return 'typescript';
  }

  if (
    /^\s*(def|class)\s+[A-Za-z_][A-Za-z0-9_]*\s*[\(:]/m.test(sample)
    || /^\s*from\s+[A-Za-z0-9_.]+\s+import\s+/m.test(sample)
  ) {
    return 'python';
  }

  if (
    /^\s*#!/.test(sample)
    || /^\s*(echo|printf|if|then|fi|for|while)\b/m.test(sample)
  ) {
    return 'bash';
  }

  return null;
}

function escapeHtml(raw: string): string {
  return raw
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;');
}

function renderPlainCodeHtml(code: string): string {
  const lines = escapeHtml(String(code || '')).split('\n');
  const body = lines
    .map((line) => `<span class="line">${line || '&#8203;'}</span>`)
    .join('\n');
  return `<pre class="shiki" style="background-color:transparent"><code>${body}</code></pre>`;
}

async function resolveLanguage(highlighter: Highlighter, lang: string, code: string): Promise<string | null> {
  const normalized = normalizeLanguageLabel(lang) || 'text';
  const inferred = normalized === 'text' ? inferLanguageFromCode(code) : null;
  const candidate = inferred || normalized;

  const loaded = new Set(highlighter.getLoadedLanguages().map((item) => String(item)));
  if (loaded.has(candidate)) return candidate;

  const shiki = await getShikiModule();
  const bundledLanguages = shiki.bundledLanguages as Record<string, unknown>;
  if (Object.prototype.hasOwnProperty.call(bundledLanguages, candidate)) {
    try {
      await highlighter.loadLanguage(candidate as any);
      return candidate;
    } catch {
      // Fall through to plaintext fallback.
    }
  }

  return null;
}

export async function highlightCode(code: string, lang: string): Promise<string> {
  const highlighter = await getHighlighter();
  const effectiveLang = await resolveLanguage(highlighter, lang, code);
  if (!effectiveLang) return renderPlainCodeHtml(code);

  const html = highlighter.codeToHtml(code, {
    lang: effectiveLang,
    theme: 'github-dark-default',
  });
  // Strip Shiki's hardcoded background-color so the app theme controls it
  return html.replace(
    /(<pre[^>]*?)style="[^"]*background-color:[^"]*"/,
    '$1style="background-color:transparent"',
  );
}

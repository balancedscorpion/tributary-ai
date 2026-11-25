const LANGUAGE_ALIASES: Record<string, string> = {
  csharp: 'c#',
  cs: 'c#',
  cplusplus: 'cpp',
  'c++': 'cpp',
  'objective-c': 'objective-c',
  objc: 'objective-c',
  js: 'javascript',
  jsx: 'jsx',
  ts: 'typescript',
  tsx: 'tsx',
  py: 'python',
  yml: 'yaml',
  sh: 'bash',
  shell: 'bash',
  zsh: 'bash',
  console: 'bash',
  powershell: 'powershell',
  ps1: 'powershell',
  md: 'markdown',
  markdown: 'markdown',
  tex: 'latex',
  latex: 'latex',
  postgres: 'sql',
  postgresql: 'sql',
  mysql: 'sql',
  sqlite: 'sql',
  plaintext: 'text',
  plain: 'text',
  txt: 'text',
};

function sanitizeToken(input: string): string {
  return input
    .trim()
    .toLowerCase()
    .replace(/^language-/, '')
    .replace(/^[^a-z0-9+#.-]+/, '')
    .replace(/[^a-z0-9+#.-]+$/, '');
}

export function normalizeLanguageLabel(input?: string | null): string | undefined {
  if (!input) return undefined;
  const firstToken = String(input).trim().split(/\s+/)[0];
  if (!firstToken) return undefined;

  const token = sanitizeToken(firstToken);
  if (!token) return undefined;

  return LANGUAGE_ALIASES[token] || token;
}

export function languageFromClassName(className?: string | null): string | undefined {
  if (!className) return undefined;
  const tokens = String(className).split(/\s+/).filter(Boolean);

  for (const token of tokens) {
    if (token.startsWith('language-')) {
      return normalizeLanguageLabel(token.slice('language-'.length));
    }
    if (token.startsWith('lang-')) {
      return normalizeLanguageLabel(token.slice('lang-'.length));
    }
  }

  return undefined;
}

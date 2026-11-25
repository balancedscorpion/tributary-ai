import React, { useMemo } from 'react';
import { cn } from '@/lib/utils';

// Compact SQL keyword set covering the dialect surface of BigQuery, Databricks,
// Trino and Snowflake — enough to make a query block readable in the inspector
// without depending on a heavyweight highlighter library.
const KEYWORDS = new Set(
  [
    'select', 'from', 'where', 'group', 'order', 'by', 'having', 'limit', 'offset',
    'join', 'inner', 'left', 'right', 'full', 'cross', 'lateral', 'on', 'using',
    'union', 'intersect', 'except', 'distinct', 'all', 'as', 'and', 'or', 'not',
    'in', 'is', 'null', 'between', 'like', 'ilike', 'true', 'false',
    'with', 'recursive', 'window', 'over', 'partition',
    'case', 'when', 'then', 'else', 'end',
    'asc', 'desc', 'nulls', 'first', 'last',
    'create', 'replace', 'table', 'view', 'temporary', 'temp', 'if', 'exists',
    'insert', 'into', 'update', 'delete', 'set', 'values', 'returning',
    'declare', 'begin', 'commit', 'rollback',
    'cast', 'interval', 'array', 'struct', 'unnest', 'pivot', 'unpivot',
    'qualify', 'rows', 'range', 'between', 'preceding', 'following', 'unbounded',
  ].map((k) => k.toLowerCase()),
);

const FUNCTIONS = new Set(
  [
    'count', 'sum', 'avg', 'min', 'max', 'array_agg', 'string_agg', 'any_value',
    'date', 'datetime', 'timestamp', 'time', 'current_date', 'current_timestamp',
    'date_trunc', 'date_diff', 'date_add', 'date_sub', 'extract', 'format_date',
    'parse_date', 'parse_timestamp', 'datetime_trunc', 'timestamp_trunc',
    'concat', 'substr', 'substring', 'lower', 'upper', 'trim', 'length', 'replace',
    'regexp_extract', 'regexp_contains', 'regexp_replace', 'split',
    'coalesce', 'ifnull', 'nullif', 'iff', 'greatest', 'least',
    'safe_divide', 'safe_cast', 'safe_multiply',
    'round', 'floor', 'ceil', 'ceiling', 'abs', 'mod', 'sqrt', 'power', 'exp', 'ln',
    'rank', 'dense_rank', 'row_number', 'lag', 'lead', 'ntile',
    'json_extract', 'json_extract_scalar', 'to_json_string', 'parse_json',
    'unnest', 'generate_array', 'generate_date_array',
    'approx_count_distinct', 'percentile_cont', 'percentile_disc',
  ].map((k) => k.toLowerCase()),
);

interface Token {
  text: string;
  kind: 'keyword' | 'function' | 'string' | 'number' | 'comment' | 'punct' | 'identifier' | 'whitespace';
}

function tokenise(sql: string): Token[] {
  const tokens: Token[] = [];
  let i = 0;
  while (i < sql.length) {
    const ch = sql[i];
    // line comment
    if (ch === '-' && sql[i + 1] === '-') {
      const end = sql.indexOf('\n', i);
      const stop = end === -1 ? sql.length : end;
      tokens.push({ kind: 'comment', text: sql.slice(i, stop) });
      i = stop;
      continue;
    }
    // block comment
    if (ch === '/' && sql[i + 1] === '*') {
      const end = sql.indexOf('*/', i + 2);
      const stop = end === -1 ? sql.length : end + 2;
      tokens.push({ kind: 'comment', text: sql.slice(i, stop) });
      i = stop;
      continue;
    }
    // string ' or "
    if (ch === "'" || ch === '"' || ch === '`') {
      let j = i + 1;
      while (j < sql.length && sql[j] !== ch) {
        if (sql[j] === '\\' && j + 1 < sql.length) j += 2;
        else j++;
      }
      tokens.push({ kind: 'string', text: sql.slice(i, Math.min(j + 1, sql.length)) });
      i = Math.min(j + 1, sql.length);
      continue;
    }
    // number
    if (/[0-9]/.test(ch)) {
      let j = i + 1;
      while (j < sql.length && /[0-9._eE+-]/.test(sql[j])) {
        // Stop at + or - if not in exponent context
        if ((sql[j] === '+' || sql[j] === '-') && sql[j - 1] !== 'e' && sql[j - 1] !== 'E') break;
        j++;
      }
      tokens.push({ kind: 'number', text: sql.slice(i, j) });
      i = j;
      continue;
    }
    // identifier / keyword / function
    if (/[A-Za-z_]/.test(ch)) {
      let j = i + 1;
      while (j < sql.length && /[A-Za-z0-9_]/.test(sql[j])) j++;
      const text = sql.slice(i, j);
      const lower = text.toLowerCase();
      let kind: Token['kind'] = 'identifier';
      if (KEYWORDS.has(lower)) kind = 'keyword';
      else if (FUNCTIONS.has(lower) && sql[j] === '(') kind = 'function';
      tokens.push({ kind, text });
      i = j;
      continue;
    }
    // whitespace
    if (/\s/.test(ch)) {
      let j = i + 1;
      while (j < sql.length && /\s/.test(sql[j])) j++;
      tokens.push({ kind: 'whitespace', text: sql.slice(i, j) });
      i = j;
      continue;
    }
    // punctuation / operator
    tokens.push({ kind: 'punct', text: ch });
    i++;
  }
  return tokens;
}

// Per-kind text colour. The colour values match the prototype's vocabulary —
// purple keywords, blue functions, green identifiers (kept as foreground), with
// neutral colors for strings/numbers/comments so the inspector stays calm.
const TOKEN_CLASS: Record<Token['kind'], string> = {
  keyword: 'text-[#7F77DD]',
  function: 'text-[#378ADD]',
  string: 'text-emerald-600',
  number: 'text-emerald-600',
  comment: 'text-muted-foreground/70 italic',
  punct: 'text-muted-foreground',
  identifier: 'text-foreground',
  whitespace: '',
};

/** Renders SQL text with line gutter and minimal token-based highlighting. */
export function SqlBlock({
  sql,
  highlightLine,
  className,
}: {
  sql: string;
  /** 1-based line that should be highlighted (for error mode). */
  highlightLine?: number | null;
  className?: string;
}) {
  const lines = useMemo(() => {
    const text = sql.replace(/\r\n/g, '\n');
    const parts = text.split('\n');
    return parts.map((line) => tokenise(line));
  }, [sql]);

  return (
    <pre
      className={cn(
        'font-mono text-[12.5px] leading-[1.65] whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 p-3 text-foreground',
        className,
      )}
    >
      <code>
        {lines.map((lineTokens, idx) => {
          const lineNo = idx + 1;
          const isHi = highlightLine != null && lineNo === highlightLine;
          return (
            <div
              key={idx}
              className={cn(
                'flex gap-3',
                isHi && 'rounded-sm bg-destructive/10 -mx-1 px-1',
              )}
            >
              <span
                className={cn(
                  'shrink-0 select-none text-right text-muted-foreground/40 tabular-nums',
                  isHi && 'text-destructive',
                )}
                style={{ width: '1.75rem' }}
              >
                {lineNo}
              </span>
              <span className="min-w-0 flex-1">
                {lineTokens.length === 0 ? (
                  <span>&nbsp;</span>
                ) : (
                  lineTokens.map((tok, j) => (
                    <span key={j} className={TOKEN_CLASS[tok.kind]}>
                      {tok.text}
                    </span>
                  ))
                )}
              </span>
            </div>
          );
        })}
      </code>
    </pre>
  );
}

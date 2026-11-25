import { useEffect, useMemo, useRef } from 'react';
import { useSharedQueryStatuses } from '@/hooks/useSharedQueryStatus';
import { collectAsyncQueryPollTargets } from '@/lib/asyncQueryPollTargets';
import type { Artifact } from '@/types';

/**
 * useQueryCompletionHydration — swaps async-query placeholder artifacts for
 * their persisted result artifacts as soon as the shared polling path reports
 * the job completed.
 *
 * The happy path is SSE: the agent calls get_query_results, the tool-result
 * envelope replaces the placeholder in artifactsById and every surface
 * (chat card, inspector panel, task bar) upgrades. But when that envelope is
 * missed — page reload mid-query, the run ending before retrieval, or the SSE
 * stream dropping — the placeholder stays forever and the explorer renders
 * blank even though the job finished. This hook closes that gap using the
 * same completion signal everything else trusts (useSharedQueryStatuses) and
 * the same hydration path the artifact panel uses (ensureArtifactLoaded →
 * GET /api/artifacts/:id), so no new state machinery is introduced. If status
 * polling gives up after a restart/eviction, we still try hydration: an already
 * materialized full artifact can recover the explorer even when the job handle
 * has disappeared.
 *
 * The result artifact is persisted by the server when the get_query_results
 * envelope lands, which can lag the job's own completion by a few agent
 * steps — hence the bounded retry instead of a single fetch.
 */

const MAX_HYDRATION_ATTEMPTS = 5;
const HYDRATION_RETRY_DELAY_MS = 2_000;

export function useQueryCompletionHydration(input: {
  artifactsById: Record<string, Artifact>;
  ensureArtifactLoaded: (artifactId: string) => Promise<Artifact | null>;
}): void {
  const { artifactsById, ensureArtifactLoaded } = input;

  const targets = useMemo(
    () => collectAsyncQueryPollTargets(artifactsById),
    [artifactsById],
  );

  const statusByKey = useSharedQueryStatuses(targets);

  const attemptsRef = useRef<Map<string, number>>(new Map());
  const timersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());
  const ensureRef = useRef(ensureArtifactLoaded);
  ensureRef.current = ensureArtifactLoaded;

  useEffect(() => {
    const timers = timersRef.current;
    return () => {
      for (const timer of timers.values()) clearTimeout(timer);
      timers.clear();
    };
  }, []);

  useEffect(() => {
    for (const target of targets) {
      const snapshot = statusByKey[target.statusKey];
      if (snapshot?.status !== 'completed' && !snapshot?.error) continue;
      if (timersRef.current.has(target.artifactId)) continue;
      const attempts = attemptsRef.current.get(target.artifactId) ?? 0;
      if (attempts >= MAX_HYDRATION_ATTEMPTS) continue;

      const hydrate = async () => {
        timersRef.current.delete(target.artifactId);
        const attempt = (attemptsRef.current.get(target.artifactId) ?? 0) + 1;
        attemptsRef.current.set(target.artifactId, attempt);
        let loaded: Artifact | null = null;
        try {
          loaded = await ensureRef.current(target.artifactId);
        } catch {
          loaded = null;
        }
        const stillPlaceholder = !loaded || loaded.metadata?.placeholder === true;
        if (stillPlaceholder && attempt < MAX_HYDRATION_ATTEMPTS && !timersRef.current.has(target.artifactId)) {
          timersRef.current.set(
            target.artifactId,
            setTimeout(() => void hydrate(), HYDRATION_RETRY_DELAY_MS),
          );
        }
      };

      void hydrate();
    }
  }, [targets, statusByKey]);
}

export default useQueryCompletionHydration;

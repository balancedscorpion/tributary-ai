import { useState, useEffect, useCallback, useRef } from 'react';
import type { AppSettings, SavedConfig } from '@/types';
import { DEFAULT_SETTINGS, STORAGE_KEY } from '@/constants';
import { migrateSettings } from '@/services/settingsMigration';

export type SettingsMutator = (latest: AppSettings) => AppSettings;

const PROFILES_STORAGE_KEY = 'chat_app_config_profiles';

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

async function fetchJson(url: string, init?: RequestInit): Promise<any> {
  const res = await fetch(url, {
    ...init,
    cache: 'no-store',
    headers: {
      Accept: 'application/json',
      ...(init?.headers || {}),
    },
  });
  const data = await readJsonSafe(res);
  if (!res.ok) {
    const msg =
      (data && typeof data === 'object' && typeof (data as any).error === 'string' && (data as any).error) ||
      (data && typeof data === 'object' && typeof (data as any).message === 'string' && (data as any).message) ||
      `HTTP ${res.status}`;
    throw new Error(msg);
  }
  return data;
}

function unwrapSettingsPayload(raw: any): any {
  if (raw && typeof raw === 'object') {
    const maybeSettings = (raw as any).settings;
    if (maybeSettings && typeof maybeSettings === 'object') return maybeSettings;
    const maybeData = (raw as any).data;
    if (maybeData && typeof maybeData === 'object') return maybeData;
  }
  return raw;
}

export function useSettings() {
  const [settings, setSettings] = useState<AppSettings>(DEFAULT_SETTINGS);
  const [isLoaded, setIsLoaded] = useState(false);
  const [profiles, setProfiles] = useState<SavedConfig[]>([]);

  // Last settings object confirmed by (or destined for) the server. Updated
  // only by the load path and the serialized save queue below — never by
  // renders — so queued mutations always rebase on the freshest truth even
  // while React state updates are still in flight.
  const serverTruthRef = useRef<AppSettings>(DEFAULT_SETTINGS);
  // Serialized write queue: settings saves run strictly one-at-a-time, and
  // mutation-style saves re-read serverTruthRef at execution time. Two rapid
  // toggles (drawer pills, Connections-tab switches) therefore compose instead
  // of the second clobbering the first with a stale full-settings payload.
  const saveQueueRef = useRef<Promise<unknown>>(Promise.resolve());

  const adoptSettings = useCallback((next: AppSettings) => {
    serverTruthRef.current = next;
    setSettings(next);
  }, []);

  useEffect(() => {
    let cancelled = false;

    const load = async () => {
      try {
        const raw = unwrapSettingsPayload(await fetchJson('/api/settings', { method: 'GET' }));
        if (cancelled) return;
        const migrated = migrateSettings(raw);
        // The backend is authoritative. Once it is reachable, retire the old
        // browser-local settings blob so it cannot reappear during a later
        // transient load failure with an obsolete hosted-tool schema.
        localStorage.removeItem(STORAGE_KEY);
        adoptSettings(migrated);
        return;
      } catch (e) {
        // Fall back to legacy localStorage for dev/offline scenarios.
        console.warn('Failed to load /api/settings; falling back to localStorage defaults.', e);
      }

      const savedSettings = localStorage.getItem(STORAGE_KEY);
      if (savedSettings) {
        try {
          const parsed = JSON.parse(savedSettings);
          if (cancelled) return;
          const migrated = migrateSettings(parsed);
          localStorage.setItem(STORAGE_KEY, JSON.stringify(migrated));
          adoptSettings(migrated);
          return;
        } catch (e) {
          console.error('Failed to parse settings', e);
        }
      }

      if (!cancelled) adoptSettings(DEFAULT_SETTINGS);
    };

    const loaded = load().finally(() => { if (!cancelled) setIsLoaded(true); });
    // Queue writes behind hydration: a mutation enqueued while /api/settings
    // is still in flight must rebase on the server's settings, not on
    // DEFAULT_SETTINGS — otherwise an early toggle would overwrite the
    // server-persisted state with defaults.
    saveQueueRef.current = saveQueueRef.current
      .then(() => loaded)
      .then(() => undefined, () => undefined);
    return () => { cancelled = true; };
  }, [adoptSettings]);

  /** Chain a task onto the serialized save queue (runs even if the previous task failed). */
  const enqueueSave = useCallback(<T,>(task: () => Promise<T>): Promise<T> => {
    const run = saveQueueRef.current.then(task, task);
    saveQueueRef.current = run.then(() => undefined, () => undefined);
    return run;
  }, []);

  const putSettings = useCallback(async (payload: AppSettings): Promise<AppSettings> => {
    // Try PUT first (preferred), fall back to POST for backends that don't implement PUT.
    let saved: any = null;
    try {
      saved = unwrapSettingsPayload(await fetchJson('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }));
    } catch (e: any) {
      const msg = String(e?.message || e);
      if (!msg.toLowerCase().includes('405')) throw e;
      saved = unwrapSettingsPayload(await fetchJson('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      }));
    }
    // Backends may return the stored settings; otherwise, keep what was sent.
    return saved ? migrateSettings(saved) : payload;
  }, []);

  /**
   * Apply a mutation to the LATEST settings and persist the result. The
   * mutator runs when its turn in the save queue comes up, against the newest
   * server truth — not against whatever settings the caller rendered with.
   * This is the shared write path for connection enable/disable from any
   * surface (chat drawer, Connections tab).
   */
  const applySettingsMutation = useCallback(async (mutator: SettingsMutator): Promise<AppSettings> => {
    return enqueueSave(async () => {
      const next = migrateSettings(mutator(serverTruthRef.current));
      const stored = await putSettings(next);
      adoptSettings(stored);
      return stored;
    });
  }, [adoptSettings, enqueueSave, putSettings]);

  // Re-pull server-persisted settings (e.g. after backend connection checks
  // wrote fresh lastCheckedAt/lastCheckOk health fields). Best-effort: on
  // failure the current in-memory settings stay untouched. Joins the save
  // queue so a reload cannot interleave with (and shadow) an in-flight write.
  const reloadSettings = useCallback(async () => {
    await enqueueSave(async () => {
      try {
        const raw = unwrapSettingsPayload(await fetchJson('/api/settings', { method: 'GET' }));
        adoptSettings(migrateSettings(raw));
      } catch {
        // keep current settings
      }
    });
  }, [adoptSettings, enqueueSave]);

  const loadProfiles = useCallback(() => {
    try {
      const stored = localStorage.getItem(PROFILES_STORAGE_KEY);
      if (!stored) { setProfiles([]); return; }
      const parsed = JSON.parse(stored);
      if (!Array.isArray(parsed)) { setProfiles([]); return; }
      const migrated: SavedConfig[] = parsed
        .map((p: any) => {
          if (!p || typeof p !== 'object') return null;
          return { ...p, settings: migrateSettings(p.settings) } as SavedConfig;
        })
        .filter(Boolean) as SavedConfig[];
      setProfiles(migrated);
      localStorage.setItem(PROFILES_STORAGE_KEY, JSON.stringify(migrated));
    } catch (e) {
      console.error('Failed to load profiles', e);
      setProfiles([]);
    }
  }, []);

  useEffect(() => {
    loadProfiles();
  }, [loadProfiles]);

  const saveSettings = useCallback(async (newSettings: AppSettings) => {
    await enqueueSave(async () => {
      const payload = migrateSettings(newSettings);
      const stored = await putSettings(payload);
      adoptSettings(stored);
    });
  }, [adoptSettings, enqueueSave, putSettings]);

  const saveProfile = useCallback((name: string, currentSettings: AppSettings) => {
    const newProfile: SavedConfig = {
      id: crypto.randomUUID(),
      name: name.trim(),
      settings: currentSettings,
      updatedAt: Date.now(),
    };
    const updated = [...profiles, newProfile];
    setProfiles(updated);
    localStorage.setItem(PROFILES_STORAGE_KEY, JSON.stringify(updated));
  }, [profiles]);

  const deleteProfile = useCallback((id: string) => {
    const updated = profiles.filter((p) => p.id !== id);
    setProfiles(updated);
    localStorage.setItem(PROFILES_STORAGE_KEY, JSON.stringify(updated));
  }, [profiles]);

  return {
    settings,
    settingsLoaded: isLoaded,
    saveSettings,
    applySettingsMutation,
    reloadSettings,
    profiles,
    loadProfiles,
    saveProfile,
    deleteProfile,
  };
}

import { displayPath } from '@/lib/approvalPresentation';

export interface LocalDirectoryPick {
  path: string;
  displayName?: string;
}

export interface LocalFilePick {
  path: string;
  displayName?: string;
}

export class LocalDirectoryPickerCanceledError extends Error {
  constructor(message = 'Folder selection was canceled.') {
    super(message);
    this.name = 'LocalDirectoryPickerCanceledError';
  }
}

export class LocalFilePickerCanceledError extends Error {
  constructor(message = 'File selection was canceled.') {
    super(message);
    this.name = 'LocalFilePickerCanceledError';
  }
}

export class LocalDirectoryPickerUnavailableError extends Error {
  constructor(message = 'Folder selection is not available in this window.') {
    super(message);
    this.name = 'LocalDirectoryPickerUnavailableError';
  }
}

export class LocalFilePickerUnavailableError extends Error {
  constructor(message = 'File selection is not available in this window.') {
    super(message);
    this.name = 'LocalFilePickerUnavailableError';
  }
}

declare global {
  interface Window {
    aiAnalyst?: {
      pickLocalDirectory?: () => Promise<string | LocalDirectoryPick | null | undefined>;
      pickLocalFile?: () => Promise<string | LocalFilePick | null | undefined>;
    };
    localFilePermissions?: {
      pickDirectory?: () => Promise<string | LocalDirectoryPick | null | undefined>;
      pickFile?: () => Promise<string | LocalFilePick | null | undefined>;
    };
  }
}

function normalizePickedPath(value: unknown): LocalDirectoryPick | null {
  if (typeof value === 'string') {
    const path = value.trim();
    return path ? { path, displayName: displayPath(path) } : null;
  }
  if (value && typeof value === 'object') {
    const raw = value as Record<string, unknown>;
    const path = String(raw.path || raw.local_root || raw.localRoot || '').trim();
    if (!path) return null;
    const displayName = String(raw.displayName || raw.name || '').trim() || displayPath(path);
    return { path, displayName };
  }
  return null;
}

async function pickViaBridge(): Promise<LocalDirectoryPick | null> {
  const pick =
    window.aiAnalyst?.pickLocalDirectory ||
    window.localFilePermissions?.pickDirectory;
  if (!pick) return null;
  const picked = normalizePickedPath(await pick());
  if (!picked) throw new LocalDirectoryPickerCanceledError();
  return picked;
}

async function pickFileViaBridge(): Promise<LocalFilePick | null> {
  const pick =
    window.aiAnalyst?.pickLocalFile ||
    window.localFilePermissions?.pickFile;
  if (!pick) return null;
  const picked = normalizePickedPath(await pick());
  if (!picked) throw new LocalFilePickerCanceledError();
  return picked;
}

async function pickViaTauriDialog(): Promise<LocalDirectoryPick | null> {
  try {
    const dialog = await import('@tauri-apps/plugin-dialog');
    if (typeof dialog.open !== 'function') return null;
    const selected = await dialog.open({
      directory: true,
      multiple: false,
      title: 'Choose a folder for local file access',
    });
    if (selected == null) {
      throw new LocalDirectoryPickerCanceledError();
    }
    const value = Array.isArray(selected) ? selected[0] : selected;
    const picked = normalizePickedPath(value);
    if (!picked) throw new LocalDirectoryPickerCanceledError();
    return picked;
  } catch (error: any) {
    if (error instanceof LocalDirectoryPickerCanceledError) throw error;
    const message = String(error?.message || error || '');
    if (
      message.includes('Cannot find module') ||
      message.includes('Failed to resolve module') ||
      message.includes('@tauri-apps/plugin-dialog')
    ) {
      return null;
    }
    return null;
  }
}

async function pickFileViaTauriDialog(): Promise<LocalFilePick | null> {
  try {
    const dialog = await import('@tauri-apps/plugin-dialog');
    if (typeof dialog.open !== 'function') return null;
    const selected = await dialog.open({
      directory: false,
      multiple: false,
      title: 'Choose a file for local file access',
    });
    if (selected == null) {
      throw new LocalFilePickerCanceledError();
    }
    const value = Array.isArray(selected) ? selected[0] : selected;
    const picked = normalizePickedPath(value);
    if (!picked) throw new LocalFilePickerCanceledError();
    return picked;
  } catch (error: any) {
    if (error instanceof LocalFilePickerCanceledError) throw error;
    const message = String(error?.message || error || '');
    if (
      message.includes('Cannot find module') ||
      message.includes('Failed to resolve module') ||
      message.includes('@tauri-apps/plugin-dialog')
    ) {
      return null;
    }
    return null;
  }
}

async function pickViaBackendDialog(): Promise<LocalDirectoryPick | null> {
  const res = await fetch('/api/local-files/pick-directory', {
    method: 'POST',
    headers: { Accept: 'application/json' },
  });
  if (res.status === 404 || res.status === 501) return null;
  const data = await res.json().catch(() => ({}));
  if (res.status === 204 || data?.canceled === true) {
    throw new LocalDirectoryPickerCanceledError();
  }
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `Folder picker failed (HTTP ${res.status}).`);
  }
  const picked = normalizePickedPath(data.directory ?? data);
  if (!picked) throw new LocalDirectoryPickerCanceledError();
  return picked;
}

async function pickFileViaBackendDialog(): Promise<LocalFilePick | null> {
  const res = await fetch('/api/local-files/pick-file', {
    method: 'POST',
    headers: { Accept: 'application/json' },
  });
  if (res.status === 404 || res.status === 501) return null;
  const data = await res.json().catch(() => ({}));
  if (res.status === 204 || data?.canceled === true) {
    throw new LocalFilePickerCanceledError();
  }
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `File picker failed (HTTP ${res.status}).`);
  }
  const picked = normalizePickedPath(data.file ?? data);
  if (!picked) throw new LocalFilePickerCanceledError();
  return picked;
}

export async function pickLocalDirectory(): Promise<LocalDirectoryPick> {
  if (typeof window === 'undefined') {
    throw new LocalDirectoryPickerUnavailableError();
  }

  const bridgePick = await pickViaBridge();
  if (bridgePick) return bridgePick;

  const tauriPick = await pickViaTauriDialog();
  if (tauriPick) return tauriPick;

  const backendPick = await pickViaBackendDialog();
  if (backendPick) return backendPick;

  throw new LocalDirectoryPickerUnavailableError(
    'Use the desktop app to choose a folder for local file access.',
  );
}

export async function pickLocalFile(): Promise<LocalFilePick> {
  if (typeof window === 'undefined') {
    throw new LocalFilePickerUnavailableError();
  }

  const bridgePick = await pickFileViaBridge();
  if (bridgePick) return bridgePick;

  const tauriPick = await pickFileViaTauriDialog();
  if (tauriPick) return tauriPick;

  const backendPick = await pickFileViaBackendDialog();
  if (backendPick) return backendPick;

  throw new LocalFilePickerUnavailableError(
    'Use the desktop app to choose a file for local file access.',
  );
}

export function isLocalDirectoryPickerCanceled(error: unknown): boolean {
  return error instanceof LocalDirectoryPickerCanceledError;
}

export function isLocalFilePickerCanceled(error: unknown): boolean {
  return error instanceof LocalFilePickerCanceledError;
}

export function isLocalPickerCanceled(error: unknown): boolean {
  return isLocalDirectoryPickerCanceled(error) || isLocalFilePickerCanceled(error);
}

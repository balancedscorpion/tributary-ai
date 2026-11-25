export function formatBytes(bytes: number | string | undefined): string {
  if (bytes === undefined || bytes === null) return '';
  const n = typeof bytes === 'string' ? parseInt(bytes, 10) : bytes;
  if (isNaN(n) || n < 0) return '';
  if (n === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const val = n / Math.pow(1024, i);
  if (i === 0) return `${Math.round(val)} ${units[i]}`;
  const decimals = val < 10 ? 2 : 1;
  return `${Number(val.toFixed(decimals))} ${units[i]}`;
}

export function formatDuration(ms: number | undefined): string {
	if (ms === undefined || ms === null || isNaN(ms)) return '';
	if (ms < 1000) return `${Math.round(ms)}ms`;
	const seconds = ms / 1000;
	if (seconds < 60) return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)}s`;
	const totalSeconds = Math.round(seconds);
	const minutes = Math.floor(totalSeconds / 60);
	const remainingSeconds = totalSeconds % 60;
	return `${minutes}m ${remainingSeconds}s`;
}

export function formatRowCount(count: number): string {
  return new Intl.NumberFormat().format(count);
}

export function formatElapsed(ms: number): string {
  const totalSeconds = Math.floor(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${seconds.toString().padStart(2, '0')}`;
}

export function formatAbsoluteDateTime(ms?: number): string {
  if (!ms || !Number.isFinite(ms)) return '';
  try {
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(ms));
  } catch {
    return new Date(ms).toISOString();
  }
}

export function formatRelativeTime(ms?: number): string {
  if (!ms || !Number.isFinite(ms)) return '';
  const diff = Date.now() - ms;
  if (!Number.isFinite(diff)) return '';
  const sec = Math.floor(diff / 1000);
  if (sec < 15) return 'just now';
  if (sec < 60) return `${sec}s ago`;
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 7) return `${day}d ago`;
  try {
    return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(new Date(ms));
  } catch {
    return `${day}d ago`;
  }
}

import type { BackupKind, State } from './api';

/** What made a backup, as the backup lists name it. */
export const backupKind: Record<BackupKind, string> = {
	manual: 'Made by hand',
	scheduled: 'Scheduled',
	'server-update': 'Before a server update',
	'manager-update': 'Before a manager update',
	migration: 'Before a database upgrade',
	restore: 'Before a restore'
};

/** What made a backup, and who when a person did. */
export function backupLabel(kind: BackupKind, by?: string): string {
	if (kind === 'manual' && by) return `Made by ${by}`;
	const label = backupKind[kind] ?? kind;
	return by ? `${label}, by ${by}` : label;
}

export function ago(ms?: number): string {
	if (!ms) return '—';
	const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
	if (s < 60) return `${s}s ago`;
	if (s < 3600) return `${Math.floor(s / 60)}m ago`;
	if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
	return `${Math.floor(s / 86400)}d ago`;
}

export function duration(fromMs?: number, now = Date.now()): string {
	if (!fromMs) return '—';
	let s = Math.max(0, Math.floor((now - fromMs) / 1000));
	const d = Math.floor(s / 86400);
	s %= 86400;
	const h = Math.floor(s / 3600);
	s %= 3600;
	const m = Math.floor(s / 60);
	if (d) return `${d}d ${h}h`;
	if (h) return `${h}h ${m}m`;
	return `${m}m ${s % 60}s`;
}

export function dateTime(unixSeconds?: number): string {
	if (!unixSeconds) return '—';
	return new Date(unixSeconds * 1000).toLocaleString();
}

export function bytes(n?: number): string {
	if (!n) return '0 B';
	const u = ['B', 'KB', 'MB', 'GB', 'TB'];
	let i = 0;
	while (n >= 1024 && i < u.length - 1) {
		n /= 1024;
		i++;
	}
	return `${n.toFixed(i ? 1 : 0)} ${u[i]}`;
}

export const stateLabel: Record<State, string> = {
	stopped: 'Stopped',
	starting: 'Starting',
	running: 'Running',
	stopping: 'Stopping',
	crashed: 'Crashed',
	updating: 'Updating'
};

export const stateColor: Record<State, string> = {
	stopped: 'bg-faint',
	starting: 'bg-warn',
	running: 'bg-ok',
	stopping: 'bg-warn',
	crashed: 'bg-bad',
	updating: 'bg-info'
};

export function steamProfile(id: string) {
	return `https://steamcommunity.com/profiles/${id}`;
}

/** Passes an outside link (a release page, a Thunderstore listing) only if it is http(s), so a javascript: URL never reaches an href. */
export function httpUrl(url?: string): string | undefined {
	return url && /^https?:\/\//i.test(url) ? url : undefined;
}

// ---- the performance charts' numbers ----

/** A round axis top at or above v, and at least floor: 1, 2, 4, 6 or 8 × 10^k,
 * so the middle line is round too. */
export function niceTop(v: number, floor: number) {
	v = Math.max(v, floor);
	const p = 10 ** Math.floor(Math.log10(v));
	return [1, 2, 4, 6, 8, 10].map((m) => m * p).find((c) => c >= v - 1e-9) ?? 10 * p;
}

const MB = 1024 * 1024;
const GB = 1024 * MB;

/** A share, with one decimal under 10%. */
export const percent = (v: number) => (v < 10 && v % 1 ? v.toFixed(1) : Math.round(v)) + '%';

/** Memory in MB, or GB from one up. */
export const memory = (v: number) => (v >= GB ? `${+(v / GB).toFixed(2)} GB` : `${Math.round(v / MB)} MB`);

/** A round axis top for memory, at least 100 MB. */
export const memoryTop = (v: number) => (v >= GB ? niceTop(v / GB, 1) * GB : niceTop(v / MB, 100) * MB);

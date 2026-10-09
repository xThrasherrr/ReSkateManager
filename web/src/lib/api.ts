export class ApiError extends Error {
	constructor(
		public status: number,
		message: string,
		public body: Record<string, unknown> = {}
	) {
		super(message);
	}
}

/** What to tell someone about e: an error's own words, or a plain sentence when it has none. */
export function message(e: unknown): string {
	if (e instanceof Error && e.message) return e.message;
	if (typeof e === 'string' && e) return e;
	return 'Something went wrong. Try again.';
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/** The body as JSON; undefined when it has none, or isn't JSON. */
function parse(text: string): unknown {
	if (!text) return undefined;
	try {
		return JSON.parse(text);
	} catch {
		return undefined;
	}
}

/** What a failed answer stands for. The manager says why in JSON; anything
 * else, such as a proxy's error page, is not shown but described. */
function failure(status: number, data: unknown): ApiError {
	const body = isRecord(data) ? data : {};
	if (typeof body.error === 'string' && body.error) return new ApiError(status, body.error, body);
	// The manager says why in JSON, so this answer came from something in front of it.
	let why = `Something between this browser and the manager turned that away (HTTP ${status}).`;
	if (status === 413) why = 'That is more than the manager, or a proxy in front of it, takes at once.';
	else if (status === 502 || status === 503 || status === 504 || (status >= 520 && status <= 527))
		why = `The manager isn't answering (HTTP ${status}). It may be restarting; try again in a moment.`;
	return new ApiError(status, why, body);
}

const unreachable = "Can't reach the manager. Check the connection, and that the manager is running.";
const notTheManager = 'Something other than the manager answered, such as a proxy asking to sign in. Reload the page.';

/** Whether a 401 from path means the session has gone, rather than a wrong password or the like. */
const sessionPath = (path: string) => !path.startsWith('/auth/') && !path.startsWith('/setup');

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	let res: Response;
	let text: string;
	try {
		res = await fetch('/api' + path, {
			method,
			// The server requires X-RSM on every write; a cross-site form cannot send it.
			headers: body === undefined ? { 'X-RSM': '1' } : { 'X-RSM': '1', 'Content-Type': 'application/json' },
			body: body === undefined ? undefined : JSON.stringify(body),
			credentials: 'same-origin'
		});
		text = await res.text();
	} catch {
		throw new ApiError(0, unreachable);
	}
	const data = parse(text);
	if (!res.ok) {
		if (res.status === 401 && sessionPath(path)) signedOut();
		throw failure(res.status, data);
	}
	if (text && data === undefined) throw new ApiError(502, notTheManager);
	// An empty answer is a success too; {} keeps it one for attempt().
	return (text ? data : {}) as T;
}

let leaving = false;

/** The session has gone (it expired, or was ended elsewhere): sign in again,
 * then come back here. A full load, so nothing of it stays in memory. */
function signedOut() {
	if (leaving) return;
	leaving = true;
	location.assign(loginPath(location.pathname + location.search));
}

/** The sign-in page, set to come back to here once signed in. */
export function loginPath(here: string) {
	return here === '/' || here.startsWith('/login') ? '/login' : `/login?next=${encodeURIComponent(here)}`;
}

/** Where to go once signed in: next, when it is a page of this panel, else the dashboard. */
export function afterSignIn(next: string | null): string {
	if (!next?.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/';
	try {
		const url = new URL(next, location.origin);
		if (url.origin !== location.origin || url.pathname === '/login' || url.pathname === '/setup') return '/';
		return url.pathname + url.search + url.hash;
	} catch {
		return '/';
	}
}

export const api = {
	get: <T>(path: string) => request<T>('GET', path),
	post: <T>(path: string, body: unknown = {}) => request<T>('POST', path, body),
	patch: <T>(path: string, body: unknown) => request<T>('PATCH', path, body),
	del: <T>(path: string) => request<T>('DELETE', path)
};

/** Waits ms, or until signal aborts, when it throws the abort. */
export const sleep = (ms: number, signal?: AbortSignal) =>
	new Promise<void>((resolve, reject) => {
		if (signal?.aborted) return reject(signal.reason);
		const t = setTimeout(resolve, ms);
		signal?.addEventListener(
			'abort',
			() => {
				clearTimeout(t);
				reject(signal.reason);
			},
			{ once: true }
		);
	});

/** Whether e is something being stopped on purpose, which is no failure to report. */
export const isAbort = (e: unknown) => e instanceof DOMException && e.name === 'AbortError';

/** A failure worth trying again: the manager or a proxy in front of it was briefly unreachable. */
export const flaky = (e: unknown) => e instanceof ApiError && (e.status === 0 || e.status >= 502);

// Work the manager does in the background, such as a backup, a restore or an
// import, since it can take longer than a proxy lets one request run.
export interface Job<T = unknown> {
	phase: 'running' | 'done' | 'failed';
	done: number; // bytes, out of
	total: number;
	error?: string;
	result?: T;
}

/** Follows the job a request started until it ends, and returns its result.
 * Aborting signal stops following it; the job itself carries on. */
export async function followJob<T>(id: string, onProgress?: (j: Job<T>) => void, signal?: AbortSignal): Promise<T> {
	for (let misses = 0; ; ) {
		await sleep(misses ? 2000 : 500, signal);
		let j: Job<T>;
		try {
			j = await api.get<Job<T>>(`/jobs/${id}`);
			misses = 0;
		} catch (e) {
			if (flaky(e) && ++misses <= 5) continue;
			throw e;
		}
		onProgress?.(j);
		if (j.phase === 'done') return j.result as T;
		if (j.phase === 'failed') throw new Error(j.error || "The manager couldn't finish that.");
	}
}

// upload sends a file or a slice of one as the request body, reporting how
// many bytes have gone. XHR rather than fetch, since fetch cannot report upload
// progress.
export function upload<T>(method: string, path: string, body: Blob, onProgress?: (sent: number) => void, signal?: AbortSignal): Promise<T> {
	return new Promise((resolve, reject) => {
		if (signal?.aborted) return reject(signal.reason);
		const xhr = new XMLHttpRequest();
		signal?.addEventListener('abort', () => xhr.abort(), { once: true });
		xhr.onabort = () => reject(signal?.reason ?? new DOMException('The upload was stopped', 'AbortError'));
		xhr.open(method, '/api' + path);
		xhr.setRequestHeader('X-RSM', '1');
		xhr.upload.onprogress = (e) => {
			onProgress?.(e.loaded);
		};
		xhr.onload = () => {
			const text = xhr.responseText;
			const data = parse(text);
			if (xhr.status >= 200 && xhr.status < 300) {
				if (text && data === undefined) return reject(new ApiError(502, notTheManager));
				return resolve((text ? data : {}) as T);
			}
			if (xhr.status === 401 && sessionPath(path)) signedOut();
			reject(failure(xhr.status, data));
		};
		xhr.onerror = () => reject(new ApiError(0, 'The upload was cut off. Try again.'));
		xhr.send(body);
	});
}

export type State = 'stopped' | 'starting' | 'running' | 'stopping' | 'crashed' | 'updating';

export interface Info {
	serverName?: string;
	map?: string;
	maxPlayers?: number;
	steamId?: string;
	publicIp?: string;
	joinCode?: string;
	password?: boolean;
}

export interface InstanceView {
	id: string;
	name: string;
	dir: string;
	autoStart: boolean;
	autoRestart: boolean;
	autoUpdate: boolean;
	// Scheduled restarts: times of day ("04:00") on the manager's clock, and
	// hours of uptime; empty and 0 are off.
	restartTimes?: string[] | null;
	restartHours?: number;
	nextRestart?: number; // unix ms, while it runs
	state: State;
	pid?: number;
	startedAt?: number;
	readyAt?: number;
	exitCode?: number;
	lastError?: string;
	players: number;
	info: Info;
	installed: boolean;
	permissions?: Perm[];
	// The release the server's program is from; missing when it isn't
	// installed or is a release the manager hasn't looked up or installed.
	serverVersion?: string;
}

export interface Entry {
	seq: number;
	stamp?: string;
	at: number;
	text: string;
	kind: string;
	tag?: string;
	name?: string;
	id?: string;
	fields?: Record<string, string>;
}

// A server's log files, by source: its own ReSkateServer.log, and the console.log the manager keeps.
export interface LogFile {
	name: string;
	size: number;
	modified: string;
}

export interface LogResult {
	entries: Entry[]; // the newest that match, oldest first
	total: number; // how many match in all
	files: Record<'server' | 'console', LogFile[] | null>;
}

export interface Player {
	id: string;
	name: string;
	admin: boolean;
	joinedAt?: number;
}

export interface Announcement {
	id: number;
	instance: string; // '*' for every server
	message: string;
	interval: number; // seconds
	enabled: boolean;
}

export interface Grant {
	roleId: number;
	role?: string;
	instance: string;
}

export interface User {
	id: number;
	username: string;
	steamId?: string;
	owner: boolean;
	disabled: boolean;
	hasPassword: boolean;
	createdAt: number;
	lastLoginAt?: number;
	grants: Grant[];
}

/** The permissions there are, as the manager names them (internal/auth/perms.go). */
export type Perm =
	| 'console.view'
	| 'console.exec'
	| 'players.view'
	| 'players.kick'
	| 'players.ban'
	| 'players.chat'
	| 'announcements.manage'
	| 'settings.view'
	| 'settings.edit'
	| 'server.lifecycle'
	| 'server.update'
	| 'ingame.admins.manage'
	| 'ingame.admin'
	| 'instances.manage'
	| 'panel.users.manage'
	| 'audit.view'
	| 'host.view';

export interface Perms {
	owner: boolean;
	global: Partial<Record<Perm, boolean>>;
	byInstance: Record<string, Partial<Record<Perm, boolean>>>;
}

export interface Role {
	id: number;
	name: string;
	permissions: string[];
	builtin: boolean;
}

export interface PermInfo {
	key: Perm;
	label: string;
	global: boolean;
}

export interface Field {
	key: string;
	label: string;
	group: string;
	// maps: map names, in order; color: "#RRGGBB"; lines: chat lines, in order; votes: CustomVote[] (#lib/votes.js)
	type: 'string' | 'int' | 'number' | 'bool' | 'enum' | 'list' | 'maps' | 'color' | 'lines' | 'votes';
	options?: string[];
	min?: number;
	max?: number;
	maxLen?: number;
	help?: string;
	info?: string; // longer explanation for the info hover
	restart?: boolean;
	secret?: boolean;
	owner?: boolean; // only owners see or change it; others aren't sent it at all
	long?: boolean; // gets a box that wraps; still one line
	default: unknown;
}

export interface Meta {
	version: string;
	permissions: PermInfo[];
	settings: Field[];
	groupInfo?: Record<string, string>; // settings group -> info hover
	managerUpdate?: { version: string; url: string }; // owners only
	timeZone?: string; // the manager's, as "UTC+02:00 (CEST)"; scheduled restarts follow it
}

// Where the manager posts alerts, and which kinds it leaves out.
export interface AlertSettings {
	webhook: string;
	off: string[];
	kinds: string[];
	diskGB: number; // a drive is low with less free than this
	memPct: number; // memory is high at this much in use...
	memMinutes: number; // ...for this long, and back down once under it as long
}

// The Discord status message: a bot posts the servers' status in a channel, and keeps that message up to date.
export interface DiscordSettings {
	tokenSet: boolean; // the token itself is never sent back
	channel: string;
	joinCodes: boolean;
	title: string; // heads the message; '' is defaultTitle
	defaultTitle: string;
	invite?: string; // adds the saved token's bot to a Discord server
	status: {
		channel?: string; // the channel's name
		link?: string; // the message in Discord
		updated?: number; // unix ms of the last post or edit
		problem?: string; // why the last try failed
		stopped?: boolean; // it won't try again until the settings change
	};
}

// How many days the audit log and player history are kept; 0 keeps them for good.
export interface Retention {
	auditDays: number;
	playerDays: number;
}

// When scheduled backups run, and how many of each kind of automatic backup are kept.
export interface BackupSettings {
	every: number; // hours; 0 is off
	keep: number;
	mods: boolean; // scheduled backups hold the mods' files too
}

export type BackupKind = 'manual' | 'scheduled' | 'server-update' | 'manager-update' | 'migration' | 'restore';

export interface BackupMod {
	folder: string;
	title?: string;
	version?: string;
	package?: string;
	disabled?: boolean;
	shared?: boolean;
}

// A backup in the manager's backups folder.
export interface BackupInfo {
	name: string;
	size: number;
	kind: BackupKind;
	created: number; // unix seconds
	version: string; // the manager's that made it
	by?: string;
	database: boolean;
	servers: { id: string; name: string; files: string[]; mods: BackupMod[]; modFiles: boolean }[];
	shared?: BackupMod[];
	sharedFiles?: boolean;
	error?: string; // why it can't be read
}

// A backup that holds one server, as its Server page lists them.
export interface ServerBackup {
	name: string;
	kind: BackupKind;
	created: number;
	version: string;
	by?: string;
	size: number;
	files: string[];
	mods: BackupMod[];
	modFiles: boolean;
}

export interface Release {
	tag: string;
	version: string;
	url: string;
	sha256: string;
	size: number;
	exeSha256: string;
	checkedAt: number;
}

export interface UpdateStatus {
	supported: boolean;
	latest?: Release;
	latestError?: string;
	exeSha256?: string;
	installed: boolean;
	version?: string; // the installed release, when the manager knows it
	upToDate: boolean;
	job?: {
		phase: string;
		version?: string;
		done?: number;
		total?: number;
		error?: string;
		by?: string;
		started: number;
		finished?: number;
	};
}

export interface AuditEntry {
	id: number;
	at: number;
	userId?: number;
	username?: string;
	instance?: string;
	action: string;
	detail?: string;
	ip?: string;
}

export interface ModMap {
	name: string;
	asset: string;
	shadowed?: boolean; // an earlier mod or a retail map has the same level
}

export interface Mod {
	folder: string;
	title: string;
	author?: string;
	version?: string;
	description?: string;
	maps: ModMap[];
	problem?: string;
	disabled?: boolean; // moved to DisabledMods, where the server does not look
	shared?: boolean; // a link to the shared mods, which update and delete it
	package?: string; // the Thunderstore package, Namespace-Name
}

export interface ModList {
	folder: string;
	retail: string[];
	mods: Mod[];
	loaded: string[] | null; // the running server's maps; null while stopped
	map?: string | null; // the map setting
	pool?: string[] | null; // the map pool; empty means every map
	// null with the shared mods turned off in the manager. own: the server's
	// own copies that keep a shared mod of the same folder out.
	shared: { use: SharedUse; own: string[] } | null;
}

// How a server uses the shared mods: not at all, all of them (ones shared
// later load at once), or the ones turned on there (ones shared later arrive off).
export type SharedUse = 'off' | 'all' | 'pick';

// A server, as the shared mods page sees it.
export interface SharedModsServer {
	id: string;
	name: string;
	use: SharedUse;
	canEdit: boolean; // the caller may change use, and enable or disable its shared mods
	map?: string | null; // its map setting
	pool?: string[] | null; // its map pool
	// Shared mods it loads, ones turned off there, and ones it keeps its own copy
	// of. One in none of them is linked when the server next starts.
	enabled: string[];
	disabled: string[];
	own: string[];
}

export interface SharedModList {
	folder: string;
	mods: Mod[];
	servers: SharedModsServer[];
}

// What Thunderstore knows about an installed mod, by folder.
export interface ModUpdate {
	package: string;
	url: string;
	latest: string;
	newer: boolean; // latest is newer than the installed version
}

// A package on Thunderstore, as the mod browser shows it.
export interface StoreMod {
	name: string; // Name_With_Underscores
	owner: string;
	fullName: string; // Namespace-Name
	url: string;
	description: string;
	icon: string;
	website?: string;
	categories: string[];
	rating: number;
	downloads: number; // of every version
	created: number; // unix ms
	updated: number;
	deprecated?: boolean;
	nsfw?: boolean;
	versions: StoreVersion[]; // newest first
	maps: ModMap[] | null; // the newest version's, once someone has looked inside it
	mapsProblem?: string;
}

export interface StoreVersion {
	version: string;
	size: number;
	downloads: number;
	created: number;
	dependencies?: string[]; // Namespace-Name-1.2.3
}

// A mod being installed from Thunderstore, as the manager reports it.
export interface ModJob {
	phase: 'queued' | 'downloading' | 'installing' | 'done' | 'failed'; // queued: waiting for the updates before it
	package: string;
	version: string;
	done: number;
	total: number;
	error?: string;
	folder?: string; // where the mod landed
}

/** One of the jobs updating every mod started, in the order they run. */
export interface UpdateJob {
	id: string;
	folder: string;
	title: string;
	version: string;
}

// mapChoices lists the maps the map setting can take: retail first, then each
// mod's. While the server runs, a mod map it has not loaded yet needs a restart.
export function mapChoices(list: ModList) {
	const loaded = list.loaded?.map((m) => m.toLowerCase());
	const pending = (name: string) => !!loaded && !loaded.includes(name.toLowerCase());
	const mods = list.mods
		.filter((m) => !m.disabled)
		.map((m) => ({ folder: m.folder, title: m.title, maps: m.maps.filter((x) => !x.shadowed).map((x) => ({ name: x.name, pending: pending(x.name) })) }))
		.filter((m) => m.maps.length);
	// Maps the server still has from a mod removed since it started.
	const known = new Set([...list.retail, ...mods.flatMap((m) => m.maps.map((x) => x.name))].map((n) => n.toLowerCase()));
	const removed = [...new Set(list.loaded ?? [])].filter((n) => !known.has(n.toLowerCase()));
	return { retail: list.retail, mods, removed };
}

export type MapChoices = ReturnType<typeof mapChoices>;

// The mod's maps a server's map pool names, in pool order. The server won't
// start while its pool names a map it doesn't have.
export function poolMapsFrom(mod: Mod, pool: string[] | null | undefined) {
	const names = new Set(mod.maps.filter((m) => !m.shadowed).map((m) => m.name.toLowerCase()));
	return (pool ?? []).filter((m) => names.has(m.toLowerCase()));
}

export interface PerfPoint {
	at: number; // bucket start, unix seconds
	run: number; // the process's start, unix seconds
	cpu: number; // percent of the whole machine, averaged
	cpuMax: number;
	mem: number; // bytes, averaged
	players: number; // most at once
}

export interface PerfRun {
	run: number;
	first: number; // first and last sample
	last: number;
	cpu: number;
	cpuMax: number;
	mem: number;
	memMax: number;
	players: number;
}

export interface Perf {
	since: number;
	bucket: number; // seconds each point covers
	interval: number; // seconds between samples
	points: PerfPoint[];
	runs: PerfRun[];
	latest: { at: number; run: number; cpu: number; mem: number; players: number } | null; // the newest sample
	network: NetPoint[];
}

/**
 * The server's own network summaries within one bucket: once a minute while
 * anyone is on, with "Log player activity" on. Fields are null when no line
 * in the bucket had them (servers before 1.1.5).
 */
export interface NetPoint {
	at: number; // bucket start, unix seconds
	players: number; // most at once
	out: number; // KB/s, averaged
	outMax: number;
	in: number;
	inMax: number;
	ping: number; // the worst player's, ms
	queued: number | null; // KB waiting to be sent, the most
	queueMs: number | null; // how long a message would wait, the longest
	failed: number | null; // sends that failed in the bucket
	skipped: number | null;
	dropped: number | null;
	busy: number | null; // percent of the server's main loop, the most
	passMs: number | null; // the loop's longest pass
	gapMs: number | null; // and its longest gap between passes
}

/** One reading of the machine the manager runs on, with its servers summed up. */
export interface HostSample {
	at: number; // unix seconds
	cpu: number; // percent of the whole machine
	memTotal: number; // bytes
	memUsed: number; // total minus available
	memCached: number; // cache the system can hand back; 0 on Windows
	limitMax: number; // the manager's cgroup memory limit; 0 without one
	limitUsed: number;
	serversCpu: number;
	serversMem: number;
	managerMem: number;
	running: number;
	servers: number;
	players: number;
}

/** A bucket of host samples: averages, with running, servers and players the most at once. */
export interface HostPoint extends HostSample {
	cpuMax: number;
}

export interface HostServer {
	id: string;
	name: string;
	cpu: number; // average while it ran
	cpuMax: number;
	mem: number; // average while it ran
	memMax: number;
	points: { at: number; cpu: number; mem: number }[]; // its share of each bucket, stacking up to serversCpu and serversMem
}

export interface HostPerf {
	since: number;
	bucket: number;
	interval: number;
	points: HostPoint[];
	servers: HostServer[];
	latest: HostSample | null;
}

/** A drive the manager keeps things on. On Linux, used and free leave out the blocks kept for root. */
export interface DiskSpace {
	volume: string; // its mount point, or its drive on Windows
	total: number;
	used: number;
	free: number; // what the manager can still write
}

/** What the manager keeps: servers (program files and own mods), logs, shared mods, backups, cache (server releases), data (database and settings). */
export type DiskKind = 'servers' | 'logs' | 'shared' | 'backups' | 'cache' | 'data';

export interface HostDisk {
	at: number; // when the parts were measured, unix seconds; 0 before the first measurement
	measuring: boolean;
	drives: DiskSpace[]; // read on each request, the manager's own folder first
	parts: { kind: DiskKind; volume: string; size: number }[];
	servers: { id: string; name: string; volume: string; files: number; mods: number; logs: number }[];
	lowFree: number; // a drive with less free space is low, as the disk alert sees it
}

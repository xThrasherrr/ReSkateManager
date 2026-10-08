import { api, flaky, followJob, message, sleep, upload, type ModJob, type UpdateJob } from './api';

export interface Upload {
	id: number;
	instance: string; // or SHARED
	server: string;
	// A zip sent from here, a mod updated from Thunderstore, or one installed from it.
	kind: 'upload' | 'update' | 'install';
	name: string;
	file?: File; // uploads
	folder?: string; // updates: the mod's folder; once done, where the mod landed
	package?: string; // installs: Namespace-Name
	version?: string; // updates and installs: the version being installed
	job?: string; // an update the manager queued itself, when updating every mod
	status: 'queued' | 'uploading' | 'downloading' | 'installing' | 'done' | 'failed';
	progress: number; // 0-1 while uploading or downloading
	replaced?: boolean;
	error?: string;
}

/** The instance of an upload to the shared mods; no server's id can be it. */
export const SHARED = '*';

/** Where the mods of instance, or the shared mods, are in the API. */
export const modsPath = (instance: string) => (instance === SHARED ? '/shared-mods' : `/instances/${instance}/mods`);

/** What updating every mod says once the manager has queued them. */
export const updatedAll = ({ started, running }: { started: number; running: number }) =>
	started
		? `Updating ${started} mod${started === 1 ? '' : 's'}, one at a time; they carry on if you close the panel`
		: running
			? `${running} update${running === 1 ? ' is' : 's are'} already under way`
			: 'Everything is already up to date';

/** Whether it can be sent again: anything from Thunderstore, or a zip. */
const sendable = (u: Upload) => u.kind !== 'upload' || !!u.file?.name.toLowerCase().endsWith('.zip');

const running = (u: Upload) => u.status === 'queued' || u.status === 'uploading' || u.status === 'downloading' || u.status === 'installing';

/**
 * Sends file in chunks to the uploads at base, since a proxy in front of the
 * panel can turn away big requests (Cloudflare stops at 100 MB), and returns
 * the answer to the last chunk. A chunk that fails on the way is sent again.
 * onProgress hears how much has gone, and whether that was all of it, after
 * which the manager is busy with the file. Aborting signal stops sending;
 * what arrived is cleared away once it has sat idle a while.
 */
export async function sendChunks<T>(base: string, file: File, onProgress: (sent: number, all: boolean) => void, signal?: AbortSignal): Promise<T> {
	const { id, chunk } = await api.post<{ id: string; chunk: number }>(base, { name: file.name, size: file.size });
	for (let off = 0, tries = 0; ; ) {
		signal?.throwIfAborted();
		const end = Math.min(off + chunk, file.size);
		try {
			const r = await upload<T & { received?: number }>(
				'PUT',
				`${base}/${id}?offset=${off}`,
				file.slice(off, end),
				(sent) => onProgress(off + sent, end === file.size && sent >= end - off),
				signal
			);
			if (r.received === undefined) return r;
			off = r.received;
			tries = 0;
		} catch (e) {
			if (!flaky(e) || ++tries > 3) throw e;
			onProgress(off, false);
			await sleep(tries * 2000, signal);
		}
	}
}

interface Installed {
	folder: string;
	replaced: boolean;
}

/** Sends an upload's zip, then follows the job that installs it to the mod it made. */
async function sendZip(u: Upload): Promise<Installed> {
	const file = u.file!;
	const { id } = await sendChunks<{ id: string }>(`${modsPath(u.instance)}/uploads`, file, (sent, all) => {
		u.progress = sent / file.size;
		// The server unpacks the zip once it has all of it.
		u.status = all ? 'installing' : 'uploading';
	});
	return followJob<Installed>(id);
}

/**
 * Has the manager fetch a mod from Thunderstore and follows the job until the
 * mod is in. The manager downloads it on its own, since a big map takes
 * longer than a proxy lets one request run. A server that uses the shared
 * mods, when they hold the version, links it at once instead. An update the
 * manager queued already is only followed.
 */
async function fetchMod(u: Upload): Promise<Pick<ModJob, 'folder' | 'version'>> {
	const base = modsPath(u.instance);
	const start = u.job
		? { id: u.job, version: u.version! }
		: await api.post<{ id?: string; version: string; folder?: string }>(
				u.kind === 'update' ? `${base}/${encodeURIComponent(u.folder!)}/update` : `${base}/installs`,
				u.kind === 'update' ? {} : { package: u.package, version: u.version ?? '' }
			);
	u.version = start.version;
	if (!start.id) return { folder: start.folder, version: start.version };
	for (let misses = 0; ; ) {
		await sleep(misses ? 2000 : 700);
		let job: ModJob;
		try {
			job = await api.get<ModJob>(`${base}/installs/${start.id}`);
			misses = 0;
		} catch (e) {
			if (flaky(e) && ++misses <= 5) continue;
			throw e;
		}
		u.status = job.phase === 'queued' ? 'queued' : job.phase === 'downloading' ? 'downloading' : 'installing';
		u.progress = job.total ? job.done / job.total : 0;
		if (job.phase === 'done') return job;
		if (job.phase === 'failed') throw new Error(job.error || "The manager couldn't install the mod.");
	}
}

/**
 * Mod uploads and installs, kept outside any page so they carry on (and stay
 * visible) while the user moves around the panel. They run one at a time,
 * oldest first.
 */
class Uploads {
	list = $state<Upload[]>([]);
	// Bumped per server (or SHARED) each time a mod lands, so its mods page can reload.
	installed = $state<Record<string, number>>({});
	#next = 1;
	#running = false;

	active = $derived(this.list.some(running));

	add(instance: string, server: string, files: Iterable<File>) {
		for (const file of files) {
			const u: Upload = { id: this.#next++, instance, server, kind: 'upload', name: file.name, file, status: 'queued', progress: 0 };
			if (!sendable(u)) Object.assign(u, { status: 'failed', error: 'Not a .zip file' });
			this.list.push(u);
		}
		this.#run();
	}

	/** Queues an update of the mod in folder to version, from Thunderstore. */
	update(instance: string, server: string, mod: { folder: string; title: string }, version: string) {
		if (this.pending(instance, mod.folder)) return;
		this.list.push({ id: this.#next++, instance, server, kind: 'update', name: mod.title, folder: mod.folder, version, status: 'queued', progress: 0 });
		this.#run();
	}

	/**
	 * Has the manager update every mod of instance with a newer version, one
	 * after another, and follows the jobs. They carry on with the panel closed.
	 * Answers how many it started, and how many it found under way already.
	 */
	async updateAll(instance: string, server: string) {
		const { jobs, running } = await api.post<{ jobs: UpdateJob[]; running: number }>(`${modsPath(instance)}/updates`);
		for (const j of jobs) {
			this.list.push({ id: this.#next++, instance, server, kind: 'update', name: j.title, folder: j.folder, version: j.version, job: j.id, status: 'queued', progress: 0 });
		}
		this.#run();
		return { started: jobs.length, running };
	}

	/** Queues an install of a package from Thunderstore; the newest version without one. */
	install(instance: string, server: string, mod: { fullName: string; title: string }, version?: string) {
		if (this.installing(instance, mod.fullName)) return;
		this.list.push({ id: this.#next++, instance, server, kind: 'install', name: mod.title, package: mod.fullName, version, status: 'queued', progress: 0 });
		this.#run();
	}

	/** Whether an update of the mod in folder is queued or running. */
	pending(instance: string, folder: string) {
		return this.list.some((u) => u.kind === 'update' && u.instance === instance && u.folder === folder && running(u));
	}

	/** Whether an install of the package is queued or running. */
	installing(instance: string, pkg: string) {
		return this.list.some((u) => u.kind === 'install' && u.instance === instance && u.package?.toLowerCase() === pkg.toLowerCase() && running(u));
	}

	async #run() {
		if (this.#running) return;
		this.#running = true;
		for (let u; (u = this.list.find((x) => x.status === 'queued')); ) {
			const cur = u;
			try {
				if (cur.kind === 'upload') {
					cur.status = 'uploading';
					const r = await sendZip(cur);
					Object.assign(cur, { status: 'done', progress: 1, folder: r.folder, replaced: r.replaced });
				} else {
					cur.status = 'downloading';
					const job = await fetchMod(cur);
					Object.assign(cur, { status: 'done', progress: 1, folder: job.folder, version: job.version });
				}
				this.installed[cur.instance] = (this.installed[cur.instance] ?? 0) + 1;
			} catch (e) {
				Object.assign(cur, { status: 'failed', error: message(e) });
			}
		}
		this.#running = false;
	}

	canRetry(u: Upload) {
		return u.status === 'failed' && sendable(u);
	}

	retry(id: number) {
		const u = this.list.find((x) => x.id === id);
		if (!u || !this.canRetry(u)) return;
		Object.assign(u, { status: 'queued', progress: 0, error: undefined, job: undefined });
		this.#run();
	}

	/** Whether it can go from the list: anything finished, or waiting here to start. The manager runs the updates it queued either way. */
	canDismiss(u: Upload) {
		return !running(u) || (u.status === 'queued' && !u.job);
	}

	dismiss(id: number) {
		this.list = this.list.filter((u) => u.id !== id || !this.canDismiss(u));
	}

	clearFinished() {
		this.list = this.list.filter((u) => u.status !== 'done' && u.status !== 'failed');
	}
}

export const uploads = new Uploads();

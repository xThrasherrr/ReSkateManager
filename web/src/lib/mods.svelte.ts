import { api, message, type Mod, type ModUpdate } from './api';
import { newest } from './newest';
import { attempt } from './toast.svelte';
import { modsPath, updatedAll, uploads } from './uploads.svelte';

/**
 * What a Mods page shows and does: a server's mods, or the shared ones
 * (instance SHARED), what Thunderstore has newer, and uploads and updates to
 * them. place names where they go in the uploads list.
 */
export class ModsPage<L extends { mods: Mod[]; folder: string }> {
	list = $state.raw<L | null>(null);
	error = $state('');
	busy = $state(false);
	updates = $state.raw<Record<string, ModUpdate>>({});
	updatesError = $state('');
	updatingAll = $state(false);

	outdated = $derived((this.list?.mods ?? []).filter((m) => this.updates[m.folder]?.newer));
	/** A zip for here is on its way, or being unpacked. */
	uploading = $derived(
		uploads.list.some((u) => u.instance === this.instance && u.kind === 'upload' && (u.status === 'queued' || u.status === 'uploading' || u.status === 'installing'))
	);

	instance: string;
	#place: () => string;
	#begin = newest();
	#beginCheck = newest();

	constructor(instance: string, place: () => string) {
		this.instance = instance;
		this.#place = place;
	}

	async load(refresh = false) {
		const current = this.#begin();
		this.busy = true;
		try {
			const l = await api.get<L>(modsPath(this.instance));
			if (!current()) return;
			this.list = l;
			this.error = '';
			this.checkUpdates(refresh);
		} catch (e) {
			if (current()) this.error = message(e);
		} finally {
			if (current()) this.busy = false;
		}
	}

	// Thunderstore is asked apart from the list, so a slow answer never holds it up.
	async checkUpdates(refresh: boolean) {
		const current = this.#beginCheck();
		try {
			const u = await api.get<Record<string, ModUpdate>>(`${modsPath(this.instance)}/updates${refresh ? '?refresh=1' : ''}`);
			if (!current()) return;
			this.updates = u;
			this.updatesError = '';
		} catch (e) {
			if (current()) this.updatesError = message(e);
		}
	}

	/** Sends the zips; the uploads panel follows them, even after leaving the page. */
	upload(files: FileList | null | undefined) {
		if (files?.length) uploads.add(this.instance, this.#place(), files);
	}

	update(mod: Mod) {
		const up = this.updates[mod.folder];
		if (up?.newer) uploads.update(this.instance, this.#place(), mod, up.latest);
	}

	// The manager queues them itself, so they carry on if the page closes.
	async updateAll() {
		this.updatingAll = true;
		await attempt(() => uploads.updateAll(this.instance, this.#place()), updatedAll);
		this.updatingAll = false;
	}
}

import { onMount } from 'svelte';
import { api, message } from './api';
import { attempt } from './toast.svelte';

/**
 * A settings form's copy of what the manager keeps at path: loaded as the
 * component starts, and saved back to the same path. show fills the form's
 * fields from each copy that comes. Make it while a component starts.
 */
export class Setting<T> {
	saved = $state.raw<T | null>(null);
	/** Why the last load failed, or '' once one succeeds. */
	error = $state('');
	busy = $state(false);

	#path: string;
	#show: (v: T) => void;

	constructor(path: string, show: (v: T) => void) {
		this.#path = path;
		this.#show = show;
		onMount(this.load);
	}

	load = async () => {
		this.error = '';
		try {
			this.#take(await api.get<T>(this.#path));
		} catch (e) {
			this.error = message(e);
		}
	};

	/** Saves body, then shows what the manager kept; done is the toast on success. */
	async save(body: unknown, done = 'Saved') {
		this.busy = true;
		const v = await attempt(() => api.patch<T>(this.#path, body), done);
		this.busy = false;
		if (v) this.#take(v);
	}

	#take(v: T) {
		this.saved = v;
		this.#show(v);
	}
}

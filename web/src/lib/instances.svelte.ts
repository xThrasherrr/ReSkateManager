import { api, message, type InstanceView } from './api';

/** The server list for the sidebar and dashboard, polled while signed in. */
class Instances {
	// Raw: each poll replaces the list, and patch() swaps one entry.
	list = $state.raw<InstanceView[]>([]);
	loaded = $state(false);
	/** Why the last poll failed, until one succeeds; the list stays as it was. */
	error = $state('');
	#timer: ReturnType<typeof setInterval> | undefined;
	#asked = 0;
	#shown = 0;

	async refresh() {
		// The list never goes back: a slow poll answering after a refresh for
		// a server just added must not take it off again. (Not newest-only, or
		// a manager slower than the poll would never update the list.)
		const n = ++this.#asked;
		try {
			const list = await api.get<InstanceView[]>('/instances');
			if (n < this.#shown) return;
			this.#shown = n;
			this.list = list;
			this.loaded = true;
			this.error = '';
		} catch (e) {
			// A 401 has already sent the page to sign-in.
			if (n > this.#shown) this.error = message(e);
		}
	}

	start() {
		this.stop();
		this.refresh();
		this.#timer = setInterval(() => this.refresh(), 5000);
	}

	stop() {
		clearInterval(this.#timer);
	}

	byId(id: string) {
		return this.list.find((i) => i.id === id);
	}

	/** Merges a live view of one server into the list between polls. */
	patch(view: InstanceView) {
		if (!this.byId(view.id)) return;
		this.list = this.list.map((i) => (i.id === view.id ? { ...i, ...view } : i));
	}
}

export const instances = new Instances();

import type { Entry, InstanceView, Player } from './api';

// Lines kept while the page is open; the Logs tab has the rest.
const KEEP = 1000;

/** What the manager sends over the socket. Console lines come only with
 * console.view and the roster only with players.view; the count always. */
type Message =
	| { type: 'snapshot'; state: InstanceView; console?: Entry[] | null; players?: Player[] | null; count?: number }
	| { type: 'console'; entry: Entry }
	| { type: 'state'; state: InstanceView }
	| { type: 'players'; players?: Player[] | null; count?: number };

/**
 * One instance's live feed over /api/instances/{id}/ws. It reconnects with
 * backoff and replaces its state with the server's snapshot each time.
 */
export class Live {
	state = $state.raw<InstanceView | null>(null);
	console = $state.raw<Entry[]>([]);
	players = $state.raw<Player[]>([]);
	/** How many players are on, whether or not the roster may be seen. */
	count = $state(0);
	connected = $state(false);

	#ws: WebSocket | null = null;
	#closed = false;
	#retry = 0;
	#timer: ReturnType<typeof setTimeout> | undefined;
	// Console lines arrive in bursts; batch them into one update per frame.
	#pending: Entry[] = [];
	#frame = 0;

	constructor(public id: string) {
		this.#connect();
	}

	#connect() {
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		const ws = new WebSocket(`${proto}//${location.host}/api/instances/${encodeURIComponent(this.id)}/ws`);
		this.#ws = ws;
		ws.onopen = () => {
			this.connected = true;
			this.#retry = 0;
		};
		ws.onmessage = (ev) => {
			try {
				this.#handle(JSON.parse(ev.data));
			} catch {
				/* not a message this panel knows */
			}
		};
		ws.onclose = () => {
			this.connected = false;
			if (this.#closed) return;
			const delay = Math.min(1000 * 2 ** this.#retry++, 15000);
			this.#timer = setTimeout(() => this.#connect(), delay);
		};
	}

	#handle(msg: Message) {
		switch (msg.type) {
			case 'snapshot':
				this.state = msg.state;
				this.console = msg.console ?? [];
				this.players = msg.players ?? [];
				this.count = msg.count ?? this.players.length;
				this.#pending = [];
				break;
			case 'console':
				this.#pending.push(msg.entry);
				if (!this.#frame) this.#frame = requestAnimationFrame(() => this.#flush());
				break;
			case 'state':
				this.state = msg.state;
				break;
			case 'players':
				this.players = msg.players ?? [];
				this.count = msg.count ?? this.players.length;
				break;
		}
	}

	#flush() {
		this.#frame = 0;
		if (!this.#pending.length) return;
		const next = this.console.concat(this.#pending);
		this.#pending = [];
		this.console = next.length > KEEP ? next.slice(next.length - KEEP) : next;
	}

	close() {
		this.#closed = true;
		clearTimeout(this.#timer);
		if (this.#frame) cancelAnimationFrame(this.#frame);
		this.#ws?.close();
	}
}

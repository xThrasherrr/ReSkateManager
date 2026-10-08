import { isAbort, message } from './api';

export interface Toast {
	id: number;
	kind: 'ok' | 'error' | 'info';
	text: string;
	ms: number; // how long it stays, once nothing holds it
}

/** Toasts on screen at once; a new one past it pushes out the oldest. */
const KEEP = 4;

let raiser: (() => void) | null = null;

/** Where the layout says how to put the toasts back on top, over a dialog
 * opened since; null when it goes. */
export function onRaise(fn: (() => void) | null) {
	raiser = fn;
}

/** Puts the toasts back on top, as a dialog that opens covers them. */
export function raiseToasts() {
	raiser?.();
}

class Toasts {
	list = $state<Toast[]>([]);
	/** The newest of each kind, for screen readers: errors at once, the rest when there's a pause. */
	said = $state.raw({ polite: { id: 0, text: '' }, assertive: { id: 0, text: '' } });
	#next = 1;
	#timers = new Map<number, ReturnType<typeof setTimeout>>();
	#held = false;

	push(kind: Toast['kind'], text: string, ms = kind === 'error' ? 7000 : 3500) {
		const id = this.#next++;
		this.list.push({ id, kind, text, ms });
		for (const old of this.list.slice(0, -KEEP)) this.dismiss(old.id);
		this.said = kind === 'error' ? { ...this.said, assertive: { id, text } } : { ...this.said, polite: { id, text } };
		if (!this.#held) this.#arm(id, ms);
		raiseToasts();
	}

	ok = (text: string) => this.push('ok', text);
	error = (err: unknown) => this.push('error', message(err));
	info = (text: string) => this.push('info', text);

	#arm(id: number, ms: number) {
		this.#timers.set(id, setTimeout(() => this.dismiss(id), ms));
	}

	/** Keeps every toast up while the pointer or focus is on one, so it can be read. */
	hold() {
		this.#held = true;
		for (const t of this.#timers.values()) clearTimeout(t);
		this.#timers.clear();
	}

	/** Lets them go again, each with its full time. */
	release() {
		this.#held = false;
		for (const t of this.list) this.#arm(t.id, t.ms);
	}

	dismiss(id: number) {
		clearTimeout(this.#timers.get(id));
		this.#timers.delete(id);
		this.list = this.list.filter((t) => t.id !== id);
	}
}

export const toast = new Toasts();

/**
 * Runs what flipping a switch asks for, and flips it back if that fails. The
 * page's state never moved, so it alone would not: Svelte leaves an input's
 * checked as it is while the value it was given stays the same.
 */
export async function flip(e: Event & { currentTarget: HTMLInputElement }, change: (on: boolean) => Promise<unknown>) {
	const input = e.currentTarget; // gone from e once the event is over
	const on = input.checked;
	if ((await change(on)) === undefined) input.checked = !on;
}

/** Runs an action, reporting failure (and optionally success) as a toast. */
export async function attempt<T>(fn: () => Promise<T>, success?: string | ((v: T) => string)): Promise<T | undefined> {
	try {
		const v = await fn();
		if (success) toast.ok(typeof success === 'function' ? success(v) : success);
		return v;
	} catch (e) {
		if (!isAbort(e)) toast.error(e);
		return undefined;
	}
}

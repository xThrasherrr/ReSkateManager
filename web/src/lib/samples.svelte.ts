import { untrack } from 'svelte';
import { api, message } from './api';

/** The time ranges the performance pages show, with the step between their axis labels in seconds. */
export const ranges = [
	['1h', '1 hour', 600],
	['6h', '6 hours', 3600],
	['24h', '24 hours', 4 * 3600],
	['7d', '7 days', 86400]
] as const;

export type Range = (typeof ranges)[number][0];

/** What every answer of the manager's sample endpoints has. */
interface Sampled {
	since: number; // unix seconds
	bucket: number; // seconds each point sums up
	interval: number; // seconds between samples
}

/**
 * Samples over a time range from one of the manager's endpoints, asked again
 * every 30 s to keep up with its sampling. Make it while a component starts.
 */
export class Samples<T extends Sampled> {
	range = $state<Range>('1h');
	data = $state.raw<T | null>(null);
	/** When the newest answer came: the right edge of the charts. */
	until = $state(Date.now() / 1000);
	error = $state('');

	since = $derived(this.data?.since ?? this.until - 3600);
	bucket = $derived(this.data?.bucket ?? 30);
	rangeName = $derived(ranges.find(([key]) => key === this.range)?.[1] ?? '');

	/** Axis labels on round local times. */
	ticks = $derived.by(() => {
		const step = ranges.find(([key]) => key === this.range)?.[2] ?? 600;
		const tz = new Date().getTimezoneOffset() * 60;
		const out: { at: number; label: string }[] = [];
		for (let t = Math.ceil((this.since - tz) / step) * step + tz; t <= this.until; t += step) {
			const d = new Date(t * 1000);
			out.push({ at: t, label: step >= 86400 ? d.toLocaleDateString([], { weekday: 'short', day: 'numeric' }) : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) });
		}
		return out;
	});

	#path: (range: Range) => string;

	constructor(path: (range: Range) => string) {
		this.#path = path;
		// Ask on a new range, then keep up with the manager's sampling.
		$effect(() => {
			const r = this.range;
			untrack(() => this.load(r));
			const t = setInterval(() => this.load(r, true), 30_000);
			return () => clearInterval(t);
		});
	}

	/** Where a bucket's point sits: in the middle of the time it sums up. */
	mid = (at: number) => at + this.bucket / 2;

	async load(r: Range = this.range, quiet = false) {
		try {
			const d = await api.get<T>(this.#path(r));
			if (r !== this.range) return;
			this.data = d;
			this.until = Date.now() / 1000;
			this.error = '';
		} catch (err) {
			// A missed poll waits for the next one, unless there is nothing to show.
			if (r === this.range && (!quiet || !this.data)) this.error = message(err);
		}
	}
}

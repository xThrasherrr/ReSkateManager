import { api, message, type ModMap, type StoreMod } from './api';

/** What a look inside a version's zip found. Unknown: Thunderstore keeps the zip where the manager cannot look inside it. */
export type MapsLook =
	| { state: 'looking' }
	| { state: 'done'; maps: ModMap[]; problem?: string }
	| { state: 'unknown'; reason: string }
	| { state: 'failed'; error: string };

const key = (mod: StoreMod, version: string) => `${mod.fullName}-${version}`.toLowerCase();

/**
 * Thunderstore's ReSkate packages for the mod browser, kept while the panel is
 * open so going back to the browser is instant. The maps each package adds
 * are found by looking inside its zip on Thunderstore, a few at a time.
 */
class Thunderstore {
	mods = $state.raw<StoreMod[] | null>(null);
	error = $state('');
	loading = $state(false);
	fetched = $state(0);
	looks = $state<Record<string, MapsLook>>({});
	#queue: { mod: StoreMod; version: string }[] = [];
	#running = 0;

	async load(refresh = false) {
		if (this.loading) return;
		this.loading = true;
		try {
			const r = await api.get<{ mods: StoreMod[]; fetched: number }>(`/thunderstore/packages${refresh ? '?refresh=1' : ''}`);
			this.mods = r.mods;
			this.fetched = r.fetched;
			this.error = '';
		} catch (e) {
			this.error = message(e);
		} finally {
			this.loading = false;
		}
	}

	/** What is known of the maps a version adds; the newest by default. */
	maps(mod: StoreMod, version = mod.versions[0]?.version ?? ''): MapsLook | undefined {
		const look = this.looks[key(mod, version)];
		if (look) return look;
		if (mod.maps && version === mod.versions[0]?.version) return { state: 'done', maps: mod.maps, problem: mod.mapsProblem };
	}

	/** Queues a look inside a version's zip, unless one is known or on its way. */
	look(mod: StoreMod, version = mod.versions[0]?.version ?? '') {
		const known = this.maps(mod, version);
		if (!version || (known && known.state !== 'failed')) return;
		this.looks[key(mod, version)] = { state: 'looking' };
		this.#queue.push({ mod, version });
		this.#next();
	}

	/** Looks still waiting or under way. */
	get pending() {
		return Object.values(this.looks).filter((l) => l.state === 'looking').length;
	}

	#next() {
		while (this.#running < 3 && this.#queue.length) {
			const { mod, version } = this.#queue.shift()!;
			this.#running++;
			const q = new URLSearchParams({ package: mod.fullName, version });
			api
				.get<{ maps: ModMap[] | null; problem?: string; unknown?: string }>(`/thunderstore/maps?${q}`)
				.then(
					(r) =>
						(this.looks[key(mod, version)] = r.maps
							? { state: 'done', maps: r.maps, problem: r.problem || undefined }
							: { state: 'unknown', reason: r.unknown || 'The manager cannot look inside this mod.' })
				)
				.catch((e) => (this.looks[key(mod, version)] = { state: 'failed', error: message(e) }))
				.finally(() => {
					this.#running--;
					this.#next();
				});
		}
	}
}

export const thunderstore = new Thunderstore();

/** Thunderstore names use underscores for spaces. */
export const modName = (mod: StoreMod) => mod.name.replaceAll('_', ' ');

/** Whether dotted version a is newer than b, as the manager decides it. Versions that do not parse are never newer. */
export function newerVersion(a: string, b?: string): boolean {
	const parse = (s?: string) => {
		const parts = (s ?? '').replace(/^v/, '').split('.');
		return parts.every((p) => /^\d+$/.test(p)) ? parts.map(Number) : null;
	};
	const x = parse(a);
	const y = parse(b);
	if (!x || !y) return false;
	for (let i = 0; i < Math.max(x.length, y.length); i++) {
		if ((x[i] ?? 0) !== (y[i] ?? 0)) return (x[i] ?? 0) > (y[i] ?? 0);
	}
	return false;
}

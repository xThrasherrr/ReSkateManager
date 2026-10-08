<script lang="ts">
	import { untrack } from 'svelte';
	import { api, message } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	interface Layer {
		key: string;
		label: string;
		category: string;
		map: string;
	}
	interface World {
		layers: Layer[];
		values: Record<string, string>;
		timeOfDay: string;
		sync: boolean;
	}

	const ctx = useLive();
	const canEdit = $derived(ctx.can('settings.edit'));

	let world = $state.raw<World | null>(null);
	let error = $state('');
	let edits = $state.raw<Record<string, string>>({});
	let tod = $state('');
	let mapFilter = $state('');
	let search = $state('');
	let showDebug = $state(false);
	let busy = $state(false);

	const begin = newest();
	async function load() {
		const current = begin();
		try {
			const w = await api.get<World>(`/instances/${ctx.id}/world`);
			if (!current()) return;
			world = w;
			tod = w.timeOfDay;
			edits = {};
			error = '';
			if (!mapFilter && w.layers[0]) mapFilter = w.layers[0].map;
		} catch (e) {
			if (current()) error = message(e);
		}
	}
	$effect(() => {
		ctx.id;
		untrack(load);
	});

	// world-layers.json names maps by their level folder.
	const mapNames: Record<string, string> = {
		bam: 'San Vansterdam',
		grom: 'Isle of Grom',
		mpr: 'Super Ultra Mega Resort',
		ftue: 'Tutorial Island',
		stadium_1: 'Stadium 1',
		stadium_2: 'Stadium 2'
	};
	const times: [string, string][] = [
		['default', "Each map's own"],
		['morning', 'Morning'],
		['noon', 'Noon'],
		['afternoon', 'Afternoon'],
		['evening', 'Evening'],
		['night', 'Night'],
		['weatherday', 'Bad weather, day'],
		['weathernight', 'Bad weather, night']
	];

	const maps = $derived([...new Set((world?.layers ?? []).map((l) => l.map))]);
	const shown = $derived(
		(world?.layers ?? []).filter(
			(l) =>
				l.map === mapFilter &&
				(showDebug || l.category !== 'Debug') &&
				(!search || `${l.label} ${l.key}`.toLowerCase().includes(search.toLowerCase()))
		)
	);
	const categories = $derived([...new Set(shown.map((l) => l.category))].sort());
	const mode = (key: string) => edits[key] ?? world?.values[key] ?? 'default';
	const changed = $derived(Object.keys(edits).filter((k) => edits[k] !== (world?.values[k] ?? 'default')));

	function set(key: string, m: string) {
		edits = { ...edits, [key]: m };
	}

	async function save(body: Record<string, unknown>) {
		busy = true;
		const ok = await attempt(() => api.post<{ reply: string }>(`/instances/${ctx.id}/world`, body), (r) => r.reply);
		busy = false;
		if (ok) load();
	}
	function saveLayers() {
		const values: Record<string, string> = {};
		for (const k of changed) values[k] = edits[k] ?? 'default';
		save({ values });
	}

	guardUnsaved(() => changed.length > 0 || (!!world && tod !== world.timeOfDay && tod !== 'custom'));
</script>

<div class="mx-auto max-w-5xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div>
		<h1 class="page-title">World</h1>
		<p class="mt-2 text-sm text-muted">Time of day and the parts of each map that can be switched on or off.</p>
	</div>

	{#if error}<LoadError {error} onretry={load} />{/if}
	{#if world && !world.layers.length}
		<p class="card p-4 text-sm text-muted">
			World layers need <code>world-layers.json</code> next to the server. Linux releases include it; on Windows, copy it from a player's
			<code>%LOCALAPPDATA%\ReSkate\cache</code> folder for the same game build.
		</p>
	{:else if world}
		{#if !world.sync}
			<p class="rounded-xl border-2 border-dashed border-warn/60 bg-warn/5 px-4 py-3 text-sm text-muted">
				World layer sync is off, so players choose their own layers. Turn it on under <a class="text-accent hover:underline" href="/s/{ctx.id}/settings">Settings</a> to apply these to everyone.
			</p>
		{/if}

		<section class="card p-4">
			<h2 class="mb-3" id="time-of-day">Time of day</h2>
			<div class="flex flex-wrap items-center gap-2">
				<select class="input min-w-0 flex-1 sm:w-64 sm:flex-none" aria-labelledby="time-of-day" bind:value={tod} disabled={!canEdit}>
					{#if world.timeOfDay === 'custom'}<option value="custom" disabled>Custom (set layer by layer)</option>{/if}
					{#each times as [key, label] (key)}<option value={key}>{label}</option>{/each}
				</select>
				{#if canEdit}
					<button class="btn btn-primary" disabled={busy || tod === world.timeOfDay || tod === 'custom'} onclick={() => save({ timeOfDay: tod })}>Apply</button>
				{/if}
			</div>
			<p class="mt-2 text-xs text-faint">Applies to every map, so it holds across map changes.</p>
		</section>

		<section class="card">
			<div class="flex flex-wrap items-center gap-2 border-b-2 border-line px-4 py-3">
				<h2 class="mr-auto">Layers</h2>
				<select class="input w-auto max-w-full" aria-label="Map" bind:value={mapFilter}>
					{#each maps as m (m)}<option value={m}>{mapNames[m] ?? m}</option>{/each}
				</select>
				<input class="input min-w-0 flex-1 sm:w-48 sm:flex-none" placeholder="Search" aria-label="Search layers" bind:value={search} />
				<label class="flex items-center gap-1.5 text-xs text-muted"
					><input type="checkbox" bind:checked={showDebug} /> Debug</label
				>
			</div>
			{#each categories as cat (cat)}
				<div class="border-b-2 border-line bg-sunken/40 px-4 pt-3 pb-1.5 font-display text-xs tracking-widest text-faint uppercase">{cat}</div>
				<table class="table">
					<tbody>
						{#each shown.filter((l) => l.category === cat) as l (l.key)}
							<tr>
								<td>
									<div>{l.label}</div>
									<div class="font-mono text-[11px] break-all text-faint">{l.key}</div>
								</td>
								<td class="w-px text-right whitespace-nowrap">
									<div class="seg" role="group" aria-label={l.label}>
										{#each ['default', 'on', 'off'] as m (m)}
											<button
												class={mode(l.key) !== m ? '' : m === 'on' ? 'seg-on !bg-ok' : m === 'off' ? 'seg-on !bg-bad' : 'seg-on'}
												aria-pressed={mode(l.key) === m}
												disabled={!canEdit}
												onclick={() => set(l.key, m)}>{m}</button
											>
										{/each}
									</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{:else}
				<p class="px-4 py-6 text-sm text-faint">No layers match.</p>
			{/each}
			{#if canEdit}
				<div class="flex items-center justify-end gap-2 px-4 py-3">
					{#if changed.length}<button class="btn" onclick={() => (edits = {})}>Undo</button>{/if}
					<button class="btn btn-primary" disabled={busy || !changed.length} onclick={saveLayers}
						>Save {changed.length || ''} change{changed.length === 1 ? '' : 's'}</button
					>
				</div>
			{/if}
		</section>
	{:else if !error}
		<Loading />
	{/if}
</div>

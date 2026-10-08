<script lang="ts">
	import type { MapChoices } from '#lib/api.js';
	import Icon from './Icon.svelte';

	// The map pool: the maps players vote between, in the order the rotation
	// takes them. Empty is every map. Like the in-game card, it keeps at least
	// one map once it has any; "Use every map" empties it.
	let {
		id,
		value = $bindable(),
		choices,
		current = '',
		map = '',
		disabled = false
	}: {
		id: string;
		value: string[];
		choices: MapChoices | null; // null until the server's maps are listed
		current?: string; // the map the running server is on
		map?: string; // the map setting
		disabled?: boolean;
	} = $props();

	const lower = (s: string) => s.toLowerCase();
	const has = (list: string[], m: string) => list.some((x) => lower(x) === lower(m));
	const pool = $derived(value ?? []);

	// Why a pool map may be trouble: the server won't start while the pool names a map it doesn't have.
	function trouble(m: string): { label: string; title: string } | null {
		if (!choices) return null;
		if (has(choices.removed, m))
			return { label: 'Removed from Mods', title: "The running server still has it, but won't start again while the pool names it." };
		const modMap = choices.mods.flatMap((mod) => mod.maps).find((x) => lower(x.name) === lower(m));
		if (modMap?.pending) return { label: 'Restart to load', title: 'Its mod was added after the server started.' };
		if (!modMap && !has(choices.retail, m)) return { label: 'Not installed', title: "No mod in Mods adds this map, so the server won't start while the pool names it." };
		return null;
	}

	function move(i: number, by: number) {
		const next = [...pool];
		const [a, b] = [next[i], next[i + by]];
		if (a === undefined || b === undefined) return;
		[next[i], next[i + by]] = [b, a];
		value = next;
	}
	function add(m: string) {
		if (m && !has(pool, m)) value = [...pool, m];
	}
	const outside = $derived(!!map && pool.length > 0 && !has(pool, map));
</script>

<div class="space-y-2">
	{#if pool.length}
		<ol class="divide-y divide-line/60 rounded-lg border-2 border-line">
			{#each pool as m, i (i)}
				{@const issue = trouble(m)}
				<li class="flex flex-wrap items-center gap-x-2 gap-y-1 py-1.5 pr-1.5 pl-3">
					<span class="w-5 shrink-0 text-right font-mono text-xs text-faint tabular-nums">{i + 1}</span>
					<span class="min-w-0 flex-1 truncate font-medium" title={m}>{m}</span>
					{#if current && lower(current) === lower(m)}<span class="sticker sticker-accent">Current map</span>{/if}
					{#if issue}<span class="sticker sticker-warn" title={issue.title}>{issue.label}</span>{/if}
					<span class="flex shrink-0">
						<button type="button" class="btn btn-ghost btn-sm px-1.5" aria-label="Move {m} up" disabled={disabled || i === 0} onclick={() => move(i, -1)}>
							<Icon name="chevron" size={14} class="rotate-180" />
						</button>
						<button
							type="button"
							class="btn btn-ghost btn-sm px-1.5"
							aria-label="Move {m} down"
							disabled={disabled || i === pool.length - 1}
							onclick={() => move(i, 1)}
						>
							<Icon name="chevron" size={14} />
						</button>
						<button
							type="button"
							class="btn btn-ghost btn-sm px-1.5 hover:text-bad"
							aria-label="Take {m} out of the pool"
							title={pool.length === 1 ? 'The pool keeps at least one map. Use every map instead.' : 'Take it out of the pool'}
							disabled={disabled || pool.length === 1}
							onclick={() => (value = pool.filter((_, j) => j !== i))}
						>
							<Icon name="x" size={14} />
						</button>
					</span>
				</li>
			{/each}
		</ol>
		{#if outside}
			<p class="text-xs text-faint">The server's map, {map}, isn't in the pool. Players can't vote for it, and the rotation moves on to map 1.</p>
		{/if}
	{:else}
		<p class="rounded-lg border-2 border-dashed border-line px-3 py-2.5 text-sm text-muted">
			<span class="font-semibold text-fg">Every map.</span> Players can vote for any map the server has, and the rotation goes through them all.{disabled
				? ''
				: ' Add a map to pick the pool yourself.'}
		</p>
	{/if}
	{#if !disabled}
		<div class="flex flex-wrap items-center gap-2">
			{#if choices}
				<select
					{id}
					class="input w-auto max-w-full min-w-0 flex-1 sm:max-w-72"
					value=""
					onchange={(e) => {
						add(e.currentTarget.value);
						e.currentTarget.value = '';
					}}
				>
					<option value="">{pool.length ? 'Add a map…' : 'Start the pool with…'}</option>
					{#if choices.retail.some((m) => !has(pool, m))}
						<optgroup label="Retail">
							{#each choices.retail.filter((m) => !has(pool, m)) as m (m)}<option value={m}>{m}</option>{/each}
						</optgroup>
					{/if}
					{#each choices.mods as mod (mod.folder)}
						{@const left = mod.maps.filter((x) => !has(pool, x.name))}
						{#if left.length}
							<optgroup label={mod.title}>
								{#each left as x, i (i)}
									<!-- The server only knows maps from mods it loaded at start. -->
									<option value={x.name} disabled={x.pending}>{x.name}{x.pending ? ' (restart to load)' : ''}</option>
								{/each}
							</optgroup>
						{/if}
					{/each}
				</select>
			{/if}
			{#if pool.length}
				<button type="button" class="btn btn-sm" onclick={() => (value = [])}>Use every map</button>
			{/if}
		</div>
	{/if}
</div>

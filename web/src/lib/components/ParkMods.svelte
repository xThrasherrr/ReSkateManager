<script lang="ts">
	import { blankParkMod, maxParkMods, parkKeys, parkModProblems, type ParkMod } from '#lib/parkmods.js';
	import Icon from './Icon.svelte';

	// Parks built in the game's Park Editor and shared as mods (ReSkate 2.0.6),
	// which the server spawns with park-mod add <name>. folders and maps are
	// what the server's Mods hold, offered as choices; a folder not copied in
	// yet can still be named.
	let {
		id,
		value = $bindable(),
		folders = [],
		maps = [],
		disabled = false
	}: { id: string; value: ParkMod[] | undefined; folders?: string[]; maps?: string[]; disabled?: boolean } = $props();

	const list = $derived(value ?? []);
	const problems = $derived(parkModProblems(list));

	function set(i: number, change: Partial<ParkMod>) {
		value = list.map((p, j) => (j === i ? { ...p, ...change } : p));
	}
</script>

<div {id} class="space-y-2">
	<datalist id="{id}-folders">{#each folders as f (f)}<option value={f}></option>{/each}</datalist>
	<datalist id="{id}-maps">{#each maps as m (m)}<option value={m}></option>{/each}</datalist>
	<datalist id="{id}-keys">{#each parkKeys as k (k)}<option value={k}></option>{/each}</datalist>
	{#if list.length}
		<ol class="space-y-2">
			{#each list as p, i (i)}
				<li class="space-y-2 rounded-lg border-2 p-3 {problems[i] ? 'border-bad/70' : 'border-line'}">
					<div class="flex flex-wrap items-end gap-2">
						<label class="block">
							<span class="label">Name</span>
							<input
								class="input w-36 font-mono"
								placeholder="street"
								maxlength="16"
								spellcheck="false"
								autocomplete="off"
								{disabled}
								value={p.name}
								oninput={(e) => set(i, { name: e.currentTarget.value.toLowerCase() })}
							/>
						</label>
						<label class="block min-w-0 flex-1">
							<span class="label">Mod folder</span>
							<input
								class="input"
								list="{id}-folders"
								placeholder="popular skate 2 street park"
								maxlength="64"
								spellcheck="false"
								autocomplete="off"
								{disabled}
								value={p.folder}
								oninput={(e) => set(i, { folder: e.currentTarget.value })}
							/>
						</label>
						<button
							type="button"
							class="btn btn-ghost btn-sm"
							{disabled}
							aria-label="Remove {p.name || 'this park mod'}"
							title="Remove"
							onclick={() => (value = list.filter((_, j) => j !== i))}
						>
							<Icon name="trash" size={14} />
						</button>
					</div>
					<div class="flex flex-wrap gap-2">
						<label class="block min-w-0 flex-1">
							<span class="label">Built on the map</span>
							<input
								class="input"
								list="{id}-maps"
								placeholder="San Vansterdam"
								spellcheck="false"
								autocomplete="off"
								{disabled}
								value={p.map}
								oninput={(e) => set(i, { map: e.currentTarget.value })}
							/>
						</label>
						<label class="block w-36">
							<span class="label">Key</span>
							<input
								class="input font-mono"
								list="{id}-keys"
								placeholder="its only park"
								maxlength="32"
								spellcheck="false"
								autocomplete="off"
								{disabled}
								value={p.key}
								oninput={(e) => set(i, { key: e.currentTarget.value.toLowerCase() })}
							/>
						</label>
					</div>
					{#if problems[i]}<p class="text-xs text-bad">{problems[i]}</p>{/if}
				</li>
			{/each}
		</ol>
	{:else}
		<p class="text-sm text-faint">No park mods.</p>
	{/if}
	<div class="flex items-center gap-2">
		<button type="button" class="btn btn-sm" disabled={disabled || list.length >= maxParkMods} onclick={() => (value = [...list, blankParkMod()])}>
			<Icon name="plus" size={14} />Add a park mod
		</button>
		<span class="ml-auto text-xs text-faint tabular-nums">{list.length}/{maxParkMods}</span>
	</div>
</div>

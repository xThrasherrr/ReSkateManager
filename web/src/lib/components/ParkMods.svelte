<script lang="ts">
	import { blankParkMod, parkKeys, parkModProblems, type ParkMod } from '#lib/parkmods.js';
	import Icon from './Icon.svelte';
	import RowList from './RowList.svelte';

	// Parks built in the game's Park Editor and shared as mods (ReSkate 2.0.6),
	// which the server spawns with park-mod add <name>. folders and maps are
	// what the server's Mods hold, offered as choices; a folder not copied in
	// yet can still be named.
	let {
		id,
		value = $bindable(),
		max,
		folders = [],
		maps = [],
		disabled = false
	}: { id: string; value: ParkMod[] | undefined; max?: number; folders?: string[]; maps?: string[]; disabled?: boolean } = $props();

	const problems = $derived(parkModProblems(value ?? []));
</script>

<RowList {id} bind:value {max} blank={blankParkMod} {problems} empty="No park mods." addLabel="Add a park mod" {disabled}>
	{#snippet head()}
		<datalist id="{id}-folders">{#each folders as f (f)}<option value={f}></option>{/each}</datalist>
		<datalist id="{id}-maps">{#each maps as m (m)}<option value={m}></option>{/each}</datalist>
		<datalist id="{id}-keys">{#each parkKeys as k (k)}<option value={k}></option>{/each}</datalist>
	{/snippet}
	{#snippet row(p, _, set, remove)}
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
					oninput={(e) => set({ name: e.currentTarget.value.toLowerCase() })}
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
					oninput={(e) => set({ folder: e.currentTarget.value })}
				/>
			</label>
			<button type="button" class="btn btn-ghost btn-sm" {disabled} aria-label="Remove {p.name || 'this park mod'}" title="Remove" onclick={remove}>
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
					oninput={(e) => set({ map: e.currentTarget.value })}
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
					oninput={(e) => set({ key: e.currentTarget.value.toLowerCase() })}
				/>
			</label>
		</div>
	{/snippet}
</RowList>

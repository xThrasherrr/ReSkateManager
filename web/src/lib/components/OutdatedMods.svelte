<script lang="ts">
	import type { Mod } from '#lib/api.js';
	import Icon from './Icon.svelte';

	// Which mods Thunderstore has newer, with Update all; or why it couldn't be asked.
	let {
		outdated,
		canUpdateAll,
		busy,
		onupdateall,
		error
	}: { outdated: Mod[]; canUpdateAll: boolean; busy: boolean; onupdateall: () => void; error: string } = $props();
</script>

{#if outdated.length}
	<div class="flex flex-wrap items-center gap-3 rounded-xl border-2 border-dashed border-accent/60 bg-accent/5 px-4 py-3 text-sm">
		<span class="flex-1 text-muted">
			{outdated.length === 1 ? `${outdated[0]?.title} has` : `${outdated.length} mods have`} a newer version on Thunderstore. Players need the same version to join.
		</span>
		{#if canUpdateAll}
			<button class="btn btn-primary btn-sm" disabled={busy} onclick={onupdateall}><Icon name="download" size={12} /> Update all</button>
		{/if}
	</div>
{/if}
{#if error}
	<p class="flex items-center gap-1.5 text-xs text-faint"><Icon name="alert" size={12} /> Could not check for updates: {error}</p>
{/if}

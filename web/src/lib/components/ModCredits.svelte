<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { Mod, ModUpdate } from '#lib/api.js';

	// A mod's version and author beside its title, and whether it's the newest;
	// children add the page's own marks.
	let { mod, up, children }: { mod: Mod; up?: ModUpdate; children?: Snippet } = $props();
</script>

{#if mod.version}<span class="text-xs text-muted tabular-nums">v{mod.version}</span>{/if}
{#if mod.author}<span class="text-xs text-muted">by {mod.author}</span>{/if}
{#if up && !up.newer}<span class="text-xs text-ok" title="The newest version on Thunderstore">Up to date</span>{/if}
{@render children?.()}

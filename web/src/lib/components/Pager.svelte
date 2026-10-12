<script lang="ts">
	import { pageSizes, type Paged } from '#lib/paged.svelte.js';
	import Icon from './Icon.svelte';

	// Under a long list: how many rows to show, and which page. Nothing while
	// the whole list fits the smallest page.
	let { paged, label = 'Rows to show' }: { paged: Paged<unknown>; label?: string } = $props();
</script>

{#if paged.total > pageSizes[0]}
	<div class="flex flex-wrap items-center justify-between gap-2 border-t-2 border-line px-4 py-2">
		<div class="flex items-center gap-2">
			<span class="text-xs text-faint">Show</span>
			<div class="seg" role="group" aria-label={label}>
				{#each pageSizes as n (n)}
					<button class={paged.size === n ? 'seg-on' : ''} aria-pressed={paged.size === n} onclick={() => paged.resize(n)}>{n}</button>
				{/each}
			</div>
		</div>
		<div class="flex items-center gap-1">
			<span class="mr-1 text-xs text-muted tabular-nums">{paged.from}–{paged.to} of {paged.total}</span>
			<button class="btn btn-sm btn-ghost" disabled={paged.page <= 1} onclick={() => paged.page--} aria-label="Previous page">
				<Icon name="chevron" size={14} class="rotate-90" />
			</button>
			<button class="btn btn-sm btn-ghost" disabled={paged.page >= paged.pages} onclick={() => paged.page++} aria-label="Next page">
				<Icon name="chevron" size={14} class="-rotate-90" />
			</button>
		</div>
	</div>
{/if}

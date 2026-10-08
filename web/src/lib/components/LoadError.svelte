<script lang="ts">
	import Icon from './Icon.svelte';

	// Why a part of a page couldn't load, with a way to ask again.
	let { error, onretry, class: klass = '' }: { error: string; onretry?: () => void; class?: string } = $props();
</script>

<div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border-2 border-bad/50 bg-bad/10 px-4 py-3 text-sm {klass}" role="alert">
	<Icon name="alert" size={14} class="text-bad" />
	<p class="min-w-0 flex-1 text-fg">{error}</p>
	{#if onretry}<button class="btn btn-sm" onclick={onretry}><Icon name="restart" size={12} /> Try again</button>{/if}
</div>

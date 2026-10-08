<script lang="ts">
	import type { State } from '#lib/api.js';
	import { stateColor, stateLabel } from '#lib/format.js';
	let { state }: { state: State } = $props();
	const pulse = $derived(state === 'starting' || state === 'stopping' || state === 'updating');
</script>

<span class="inline-flex items-center gap-1.5 rounded-full border-2 border-ink bg-sunken py-0.5 pr-2 pl-1.5 text-[11px] font-semibold tracking-wide whitespace-nowrap text-fg uppercase">
	<span class="relative flex size-2.5">
		{#if pulse}<span class="absolute inline-flex size-full animate-ping rounded-full opacity-60 {stateColor[state]}"></span>{/if}
		<span class="relative inline-flex size-2.5 rounded-full {stateColor[state]}"></span>
	</span>
	{stateLabel[state]}
</span>

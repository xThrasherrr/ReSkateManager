<script lang="ts">
	import type { Sampled } from '#lib/api.js';
	import type { Samples } from '#lib/samples.svelte.js';
	import ChartCard from './ChartCard.svelte';
	import StackChart, { type Series } from './StackChart.svelte';

	// One of a performance page's charts, under its title, over the range samples shows.
	let {
		samples,
		title,
		info,
		lines,
		layers,
		ceil,
		top,
		format,
		hover = $bindable(null)
	}: {
		samples: Samples<Sampled>;
		title: string;
		info: string;
		lines: Series[];
		layers?: Series[];
		ceil: (v: number) => number;
		top?: number;
		format: (v: number) => string;
		hover?: number | null;
	} = $props();
</script>

<ChartCard {title} {info}>
	<StackChart
		{lines}
		{layers}
		since={samples.since}
		until={samples.until}
		bucket={samples.bucket}
		ticks={samples.ticks}
		{ceil}
		{top}
		{format}
		bind:hover
		label="{title}, last {samples.rangeName}"
	/>
</ChartCard>

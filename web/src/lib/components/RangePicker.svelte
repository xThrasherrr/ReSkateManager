<script lang="ts">
	import { ranges, type Range } from '#lib/samples.svelte.js';

	// Which stretch of time the performance charts show.
	let { value = $bindable(), class: klass = '' }: { value: Range; class?: string } = $props();
</script>

<div class="seg {klass}" role="group" aria-label="Time range">
	{#each ranges as [key, label] (key)}
		<button class={value === key ? 'seg-on' : ''} aria-pressed={value === key} onclick={() => (value = key)}>{label}</button>
	{/each}
</div>

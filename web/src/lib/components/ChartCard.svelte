<script lang="ts">
	import type { Snippet } from 'svelte';
	import InfoTip from './InfoTip.svelte';

	// A chart under its title, with what it shows behind the "i".
	let { title, info, children }: { title: string; info: string; children: Snippet } = $props();
</script>

<section class="card">
	<div class="flex items-center gap-1 border-b-2 border-line px-4 py-3">
		<h2>{title}</h2>
		<InfoTip text={info} label="About {title}" />
	</div>
	<div class="px-2 py-3">{@render children()}</div>
</section>

<script lang="ts">
	import InfoTip from './InfoTip.svelte';

	// A headline figure, "of" a total when it has one, and a line or two about it.
	let { label, info, now, of, sub = [] }: { label: string; info?: string; now: string; of?: string; sub?: string[] } = $props();
</script>

<div class="card px-4 py-3">
	<div class="flex min-h-5 items-center gap-1">
		<span class="label mb-0">{label}</span>
		{#if info}<InfoTip text={info} label="About {label}" />{/if}
	</div>
	<div class="font-display text-3xl leading-tight tabular-nums">
		{now}{#if of}<span class="font-sans text-sm text-muted">&nbsp;of {of}</span>{/if}
	</div>
	{#each sub as line, i (i)}<div class="text-xs text-muted tabular-nums">{line}</div>{/each}
</div>

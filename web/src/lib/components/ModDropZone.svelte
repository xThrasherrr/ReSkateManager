<script lang="ts">
	import type { Snippet } from 'svelte';

	// A Mods page, which takes .zip files dropped anywhere on it.
	let {
		label,
		hint,
		enabled,
		onfiles,
		children
	}: { label: string; hint: string; enabled: boolean; onfiles: (files: FileList | undefined) => void; children: Snippet } = $props();

	let dragging = $state(false);
</script>

<div
	class="relative mx-auto max-w-5xl space-y-4 p-4 sm:space-y-6 sm:p-6"
	role="region"
	aria-label={label}
	ondragover={(e) => {
		if (!enabled || !e.dataTransfer?.types.includes('Files')) return;
		e.preventDefault();
		dragging = true;
	}}
	ondragleave={(e) => {
		if (!e.currentTarget.contains(e.relatedTarget as Node | null)) dragging = false;
	}}
	ondrop={(e) => {
		e.preventDefault();
		dragging = false;
		if (enabled) onfiles(e.dataTransfer?.files);
	}}
>
	{@render children()}
	{#if dragging}
		<p class="pointer-events-none absolute inset-2 z-10 grid place-items-center rounded-xl border-2 border-dashed border-accent bg-panel/90 text-sm">{hint}</p>
	{/if}
</div>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { raiseToasts } from '#lib/toast.svelte.js';
	let {
		open = $bindable(false),
		title,
		children,
		actions,
		wide = false
	}: { open?: boolean; title: string; children: Snippet; actions?: Snippet; wide?: boolean } = $props();
	let dialog: HTMLDialogElement | undefined = $state();
	const id = $props.id();
	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) {
			dialog.showModal();
			raiseToasts(); // a toast about what the dialog does must show above it
		}
		if (!open && dialog.open) dialog.close();
	});
</script>

<dialog
	bind:this={dialog}
	aria-labelledby="{id}-title"
	onclose={() => (open = false)}
	class="m-auto w-[calc(100%-1.5rem)] sm:w-[calc(100%-2rem)] {wide ? 'max-w-2xl' : 'max-w-md'} rounded-2xl border-2 border-ink bg-panel p-0 text-fg shadow-sticker-lg backdrop:bg-black/70 backdrop:backdrop-blur-[2px] open:animate-pop"
>
	{#if open}
		<h2 id="{id}-title" class="border-b-2 border-line px-4 py-3.5 text-xl sm:px-5">{title}</h2>
		<div class="px-4 py-4 sm:px-5">{@render children()}</div>
		{#if actions}
			<div class="flex flex-wrap justify-end gap-2 rounded-b-2xl border-t-2 border-line bg-sunken/50 px-4 py-3 sm:px-5">{@render actions()}</div>
		{/if}
	{/if}
</dialog>

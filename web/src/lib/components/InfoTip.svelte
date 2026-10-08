<script lang="ts">
	import Icon from './Icon.svelte';

	// An "i" beside a label that explains it: shows on hover or focus, and a click
	// (or tap) pins it open until the next click, Escape or focus moving away.
	let { text, label = 'More info' }: { text: string; label?: string } = $props();

	const id = $props.id();
	let open = $state(false);
	let pinned = $state(false);

	function close() {
		open = pinned = false;
	}

	// Keep the bubble inside the viewport on narrow screens. It is built afresh
	// each time it opens, so it is measured at its natural spot.
	function fit(tip: HTMLElement) {
		const r = tip.getBoundingClientRect();
		const over = r.right - (window.innerWidth - 16);
		if (over > 0) tip.style.translate = `${-Math.min(over, r.left - 16)}px 0`;
	}
</script>

<span class="relative inline-flex align-middle">
	<button
		type="button"
		class="inline-flex size-5 items-center justify-center rounded-full text-faint transition-colors hover:text-info {open ? 'text-info' : ''}"
		aria-label={label}
		aria-describedby={open ? id : undefined}
		aria-expanded={pinned}
		onmouseenter={() => (open = true)}
		onmouseleave={() => (open = pinned)}
		onfocus={() => (open = true)}
		onblur={close}
		onclick={() => (open = pinned = !pinned)}
		onkeydown={(e) => e.key === 'Escape' && close()}
	>
		<Icon name="info" size={14} />
	</button>
	{#if open}
		<div
			{@attach fit}
			{id}
			role="tooltip"
			class="absolute top-full left-0 z-30 mt-1.5 -ml-2 w-72 max-w-[calc(100vw-2rem)] animate-pop rounded-lg border-2 border-ink bg-raised px-3 py-2 font-sans text-xs leading-relaxed font-normal tracking-normal whitespace-pre-line text-fg normal-case shadow-sticker"
		>
			{text}
		</div>
	{/if}
</span>

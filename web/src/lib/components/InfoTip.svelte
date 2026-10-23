<script lang="ts">
	import Icon from './Icon.svelte';

	// An "i" beside a label that explains it: shows on hover or focus, and a click
	// (or tap) pins it open until the next click, Escape or focus moving away.
	let { text, label = 'More info' }: { text: string; label?: string } = $props();

	const id = $props.id();
	let open = $state(false);
	let pinned = $state(false);
	let button: HTMLButtonElement;

	function close() {
		open = pinned = false;
	}

	// The bubble sits in the top layer, beside the button, so it never makes the
	// page taller: that scrolled a tip at the bottom out from under the pointer,
	// which closed it and scrolled back, over and over. Below the button, or
	// above when there's more room there; inside the viewport; following it as
	// anything scrolls.
	function place(tip: HTMLElement) {
		tip.showPopover();
		const move = () => {
			const b = button.getBoundingClientRect();
			const { width, height } = tip.getBoundingClientRect();
			const up = b.bottom + 6 + height > innerHeight - 16 && b.top > innerHeight - b.bottom;
			tip.style.top = `${up ? b.top - 6 - height : b.bottom + 6}px`;
			tip.style.left = `${Math.max(16, Math.min(b.left - 8, innerWidth - 16 - width))}px`;
		};
		move();
		addEventListener('scroll', move, true);
		addEventListener('resize', move);
		return () => {
			removeEventListener('scroll', move, true);
			removeEventListener('resize', move);
		};
	}
</script>

<span class="inline-flex align-middle">
	<button
		bind:this={button}
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
			{@attach place}
			{id}
			popover="manual"
			role="tooltip"
			class="fixed inset-auto m-0 w-72 overflow-visible max-w-[calc(100vw-2rem)] animate-pop rounded-lg border-2 border-ink bg-raised px-3 py-2 font-sans text-xs leading-relaxed font-normal tracking-normal whitespace-pre-line text-fg normal-case shadow-sticker"
		>
			{text}
		</div>
	{/if}
</span>

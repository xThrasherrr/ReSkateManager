<script lang="ts">
	import { tick, type Snippet } from 'svelte';
	import type { Entry } from '#lib/api.js';

	let {
		entries,
		filter = 'all',
		search = '',
		empty = 'No output yet.',
		top
	}: { entries: Entry[]; filter?: string; search?: string; empty?: string; top?: Snippet } = $props();

	const all = () => true;
	const groups: Partial<Record<string, (e: Entry) => boolean>> = {
		all,
		chat: (e) => e.kind === 'chat' || e.kind === 'partychat' || e.kind === 'command',
		players: (e) => e.kind === 'join' || e.kind === 'leave',
		admin: (e) => e.kind === 'admin' || e.kind === 'input',
		anticheat: (e) => e.tag === 'anticheat',
		manager: (e) => e.kind === 'manager'
	};

	const shown = $derived.by(() => {
		const f = groups[filter] ?? all;
		const q = search.trim().toLowerCase();
		return entries.filter((e) => f(e) && (!q || e.text.toLowerCase().includes(q) || e.name?.toLowerCase().includes(q)));
	});

	function tone(e: Entry): string {
		switch (e.kind) {
			case 'input':
				return 'text-accent';
			case 'manager':
				return 'text-pink';
			case 'join':
				return 'text-ok';
			case 'leave':
				return 'text-muted';
			case 'chat':
			case 'partychat':
				return 'text-info';
			case 'command':
				return 'text-info/75';
			case 'admin':
				return 'text-warn';
			case 'ready':
			case 'joincode':
			case 'steam':
				return 'text-emerald-200';
			case 'shutdown':
				return 'text-warn';
		}
		if (e.tag === 'anticheat') return 'text-bad';
		if (e.tag === 'vote' || e.tag === 'party') return 'text-teal-300';
		if (e.tag) return 'text-indigo-200';
		return 'text-fg/90';
	}

	let box: HTMLDivElement | undefined = $state();
	let content: HTMLDivElement | undefined = $state();
	let pinned = $state(true);

	let lastTop = 0;
	function onScroll() {
		if (!box) return;
		// Only the reader scrolling up lets go of the bottom: lines laid out
		// late, or the box shrinking, fire scroll events too.
		if (box.scrollHeight - box.scrollTop - box.clientHeight < 40) pinned = true;
		else if (box.scrollTop < lastTop) pinned = false;
		lastTop = box.scrollTop;
	}

	export async function toBottom() {
		pinned = true;
		await tick();
		if (box) box.scrollTop = box.scrollHeight;
	}

	// Follow new lines while pinned to the bottom. Effects run once the DOM has
	// the new lines, so scrollHeight already counts them.
	$effect(() => {
		shown;
		if (pinned && box) box.scrollTop = box.scrollHeight;
	});

	// Rows lay out lazily (content-visibility), so the height grows after that
	// first scroll, and the box can shrink under it; stay at the bottom either
	// way. With nothing left to scroll, there is no bottom to lose.
	$effect(() => {
		if (!content || !box) return;
		const b = box;
		const ro = new ResizeObserver(() => {
			if (b.scrollHeight <= b.clientHeight) pinned = true;
			if (pinned) b.scrollTop = b.scrollHeight;
		});
		ro.observe(content);
		ro.observe(b);
		return () => ro.disconnect();
	});
</script>

<div class="relative h-full">
	<div
		bind:this={box}
		onscroll={onScroll}
		class="h-full overflow-y-auto bg-sunken px-3 py-3 font-mono [overflow-anchor:none] sm:px-4 text-[12.5px] leading-[1.55]"
	>
		<div bind:this={content}>
		{@render top?.()}
		{#each shown as e (e.seq)}
			<div class="flex gap-2 [contain-intrinsic-size:auto_1.55em] [content-visibility:auto] sm:gap-3 hover:bg-white/[0.02]">
				<span class="shrink-0 text-faint tabular-nums select-none">{e.stamp ?? ''}</span>
				<span class="min-w-0 flex-1 break-words whitespace-pre-wrap {tone(e)}"
					>{#if e.kind === 'input'}<span class="text-faint select-none">{(e.name ? e.name + ' ' : '') + '> '}</span>{/if}{#if e.kind === 'manager'}<span
							class="text-faint select-none">{'[manager] '}</span
						>{/if}{e.text}</span
				>
			</div>
		{:else}
			<div class="text-faint">{entries.length ? 'No lines match.' : empty}</div>
		{/each}
		</div>
	</div>
	{#if !pinned}
		<button class="btn btn-sm btn-primary absolute right-4 bottom-3" onclick={toBottom}>Jump to latest ↓</button>
	{/if}
</div>

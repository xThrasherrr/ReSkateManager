<script lang="ts" generics="T extends { name: string }">
	import type { Snippet } from 'svelte';
	import Icon from './Icon.svelte';

	// A list edited as a card per item, such as the owner's chat commands:
	// row draws a card's fields, and the card turns red with its problem under
	// it. examples are ready-made items offered beside Add until one of that
	// name is in the list.
	let {
		id,
		value = $bindable(),
		max,
		blank,
		problems,
		empty,
		addLabel,
		examples = [],
		exampleLabel,
		exampleTitle,
		disabled = false,
		head,
		row,
		note
	}: {
		id: string;
		value: T[] | undefined;
		max?: number;
		blank: () => T;
		problems: string[];
		empty: string;
		addLabel: string;
		examples?: T[];
		exampleLabel?: (e: T) => string;
		exampleTitle?: (e: T) => string;
		disabled?: boolean;
		head?: Snippet;
		row: Snippet<[item: T, i: number, set: (change: Partial<T>) => void, remove: () => void]>;
		note?: Snippet;
	} = $props();

	const list = $derived(value ?? []);
	const full = $derived(max !== undefined && list.length >= max);

	function set(i: number, change: Partial<T>) {
		value = list.map((x, j) => (j === i ? { ...x, ...change } : x));
	}
	const remove = (i: number) => (value = list.filter((_, j) => j !== i));
	const add = (x: T) => (value = [...list, structuredClone(x)]);
	const unused = $derived(examples.filter((e) => !list.some((x) => x.name.trim() === e.name)));
</script>

<div {id} class="space-y-2">
	{@render head?.()}
	{#if list.length}
		<ol class="space-y-2">
			{#each list as item, i (i)}
				<li class="space-y-2 rounded-lg border-2 p-3 {problems[i] ? 'border-bad/70' : 'border-line'}">
					{@render row(
						item,
						i,
						(change) => set(i, change),
						() => remove(i)
					)}
					{#if problems[i]}<p class="text-xs text-bad">{problems[i]}</p>{/if}
				</li>
			{/each}
		</ol>
		{@render note?.()}
	{:else}
		<p class="text-sm text-faint">{empty}</p>
	{/if}
	<div class="flex {examples.length ? 'flex-wrap' : ''} items-center gap-2">
		<button type="button" class="btn btn-sm" disabled={disabled || full} onclick={() => add(blank())}>
			<Icon name="plus" size={14} />{addLabel}
		</button>
		{#each unused as e (e.name)}
			<button type="button" class="btn btn-ghost btn-sm" disabled={disabled || full} title={exampleTitle?.(e)} onclick={() => add(e)}>
				<Icon name="plus" size={14} />{exampleLabel?.(e)}
			</button>
		{/each}
		{#if max !== undefined}<span class="ml-auto text-xs text-faint tabular-nums">{list.length}/{max}</span>{/if}
	</div>
</div>

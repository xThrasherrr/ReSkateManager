<script lang="ts">
	import { utf8Length } from '#lib/text.js';

	// Chat lines, one per line of the box, as the server keeps a list of them:
	// each trimmed, blank ones dropped, repeats kept. The box holds what was
	// typed, so a space or a blank line doesn't vanish under the cursor.
	let {
		id,
		value = $bindable(),
		max = 0,
		maxLen = 0,
		disabled = false,
		placeholder = ''
	}: { id: string; value: string[] | undefined; max?: number; maxLen?: number; disabled?: boolean; placeholder?: string } = $props();

	const tidy = (text: string) =>
		text
			.split('\n')
			.map((l) => l.trim())
			.filter(Boolean);
	const same = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

	let text = $state('');
	// Take up a change made elsewhere (a discard, a reload), never our own.
	$effect.pre(() => {
		const v = value ?? [];
		if (!same(tidy(text), v)) text = v.join('\n');
	});

	const lines = $derived(value ?? []);
	const long = $derived(maxLen ? lines.filter((l) => utf8Length(l) > maxLen).length : 0);
</script>

<textarea
	{id}
	class="input h-auto min-h-24 resize-y py-1.5 leading-snug"
	rows={Math.min(Math.max(lines.length + 1, 3), 10)}
	{disabled}
	{placeholder}
	value={text}
	oninput={(e) => {
		// Both at once: the effect above must never see one without the other.
		text = e.currentTarget.value;
		value = tidy(text);
	}}
></textarea>
<p class="flex justify-between gap-2 text-xs text-faint tabular-nums">
	<span class={long ? 'text-bad' : ''}>
		{#if long}{long} over {maxLen} bytes{:else if maxLen}Each up to {maxLen} bytes{/if}
	</span>
	{#if max}<span class={lines.length > max ? 'text-bad' : ''}>{lines.length}/{max}</span>{/if}
</p>

<script lang="ts">
	// How far something has got, 0 to 1; with null, that it's under way but
	// can't say how far. Striped when stripes is set too, to show it's moving.
	let {
		value,
		label,
		stripes = false,
		size = 'md',
		class: klass = ''
	}: { value: number | null; label: string; stripes?: boolean; size?: 'thin' | 'md' | 'lg'; class?: string } = $props();

	const pct = $derived(value === null ? null : Math.round(Math.min(1, Math.max(0, value)) * 100));
	const frame = { thin: 'h-1.5', md: 'h-2.5 rounded-full border-2 border-ink', lg: 'h-4 rounded-full border-2 border-ink' };
</script>

<div
	class="overflow-hidden bg-sunken {frame[size]} {klass}"
	role="progressbar"
	aria-label={label}
	aria-valuemin={0}
	aria-valuemax={100}
	aria-valuenow={pct ?? undefined}
>
	<div class="h-full bg-accent transition-[width] {pct === null || stripes ? 'tape-stripes animate-tape' : ''}" style="width: {pct ?? 100}%"></div>
</div>

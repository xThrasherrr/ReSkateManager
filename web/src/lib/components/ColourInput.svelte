<script lang="ts">
	// A colour as the server writes it, "#RRGGBB": a swatch to pick it, and the
	// hex to type or paste. Typed hex is tidied to the server's spelling, so
	// "8e5cff" reads as unchanged from "#8E5CFF".
	let { id, value = $bindable(), disabled = false }: { id: string; value: string | undefined; disabled?: boolean } = $props();

	const hex = /^#?[0-9a-f]{6}$/i;
	const tidy = (s: string) => {
		s = s.trim().toUpperCase();
		return hex.test(s) && !s.startsWith('#') ? `#${s}` : s;
	};
	// The swatch only takes a full colour; while one is half typed it keeps the last.
	let last = '#000000';
	const swatch = $derived(value && hex.test(value) ? (last = `#${value.replace('#', '').toLowerCase()}`) : last);
</script>

<div class="flex items-center gap-2">
	<input
		type="color"
		class="h-8 w-10 shrink-0 cursor-pointer rounded-lg border-2 border-line-strong bg-sunken p-0.5 disabled:cursor-default disabled:opacity-60 [&::-moz-color-swatch]:rounded-md [&::-moz-color-swatch]:border-0 [&::-webkit-color-swatch]:rounded-md [&::-webkit-color-swatch]:border-0 [&::-webkit-color-swatch-wrapper]:p-0"
		aria-label="Pick a colour"
		{disabled}
		value={swatch}
		oninput={(e) => (value = e.currentTarget.value.toUpperCase())}
	/>
	<input
		{id}
		class="input font-mono uppercase"
		maxlength="7"
		spellcheck="false"
		autocomplete="off"
		placeholder="#RRGGBB"
		{disabled}
		value={value ?? ''}
		oninput={(e) => (value = tidy(e.currentTarget.value))}
	/>
</div>

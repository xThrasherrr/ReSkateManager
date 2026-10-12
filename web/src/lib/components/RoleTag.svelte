<script lang="ts">
	// A role's tag as the game draws it: in its own colour, with text that reads on it.
	let { tag, color = '', class: klass = '' }: { tag: string; color?: string; class?: string } = $props();

	const light = $derived.by(() => {
		const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color);
		if (!m) return true;
		const c = (i: number) => parseInt(m[i] ?? '0', 16) / 255;
		return 0.2126 * c(1) + 0.7152 * c(2) + 0.0722 * c(3) > 0.5;
	});
</script>

<span class="sticker {klass}" style="background-color: {color || '#FFFFFF'};{light ? '' : ' color: #fff;'}" title="Its tag in the game">{tag}</span>

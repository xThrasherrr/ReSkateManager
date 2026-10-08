<script lang="ts">
	import { copy } from '#lib/clipboard.js';
	import Icon from './Icon.svelte';

	// A folder on the manager's machine, copied with a click.
	let { label, folder }: { label: string; folder: string } = $props();
</script>

<button
	class="flex w-full items-center gap-2 rounded-xl border-2 border-dashed border-line px-4 py-3 text-left text-sm transition-colors hover:border-accent"
	onclick={() => copy(folder)}
	title="Copy path"
>
	<span class="text-faint">{label}</span>
	<span class="min-w-0 flex-1 truncate font-mono text-xs">{folder}</span>
	<Icon name="copy" size={14} />
</button>

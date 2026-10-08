<script lang="ts">
	import type { SharedUse } from '#lib/api.js';
	import { sharedUses } from '#lib/shared.js';

	// How a server uses the shared mods: not at all, all of them, or those picked.
	let { use, label, disabled, onchoose }: { use: SharedUse; label: string; disabled: boolean; onchoose: (use: SharedUse) => void } = $props();
</script>

<div class="seg" role="group" aria-label={label}>
	{#each sharedUses as u (u.use)}
		<button class={use === u.use ? 'seg-on' : ''} aria-pressed={use === u.use} title={u.help} {disabled} onclick={() => onchoose(u.use)}>{u.label}</button>
	{/each}
</div>

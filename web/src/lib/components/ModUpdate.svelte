<script lang="ts">
	import type { Mod, ModUpdate } from '#lib/api.js';
	import { httpUrl } from '#lib/format.js';
	import Icon from './Icon.svelte';

	// A mod's newer version on Thunderstore: a button to update it, or word of
	// it where it's updated elsewhere (a link there) or by someone else; and a
	// link to its page.
	let {
		mod,
		up,
		can,
		updating,
		onupdate,
		elsewhere
	}: { mod: Mod; up?: ModUpdate; can: boolean; updating: boolean; onupdate: () => void; elsewhere?: string } = $props();
</script>

{#if up?.newer && elsewhere}
	<a class="sticker sticker-accent" href={elsewhere} title="Update it under Shared mods">v{up.latest} is out</a>
{:else if up?.newer && can}
	<button class="btn btn-primary btn-sm" disabled={updating} onclick={onupdate}>
		<Icon name="download" size={12} />
		{updating ? 'Updating…' : `Update to v${up.latest}`}
	</button>
{:else if up?.newer}
	<span class="sticker sticker-accent">v{up.latest} is out</span>
{/if}
{#if up}
	<a class="btn btn-ghost btn-sm size-7 px-0" href={httpUrl(up.url)} target="_blank" rel="noreferrer" title="Open on Thunderstore" aria-label="Open {mod.title} on Thunderstore"
		><Icon name="external" size={13} /></a
	>
{/if}

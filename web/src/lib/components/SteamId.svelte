<script lang="ts">
	import { steamProfile } from '#lib/format.js';

	// A SteamID64 linking to the Steam profile: in a column of its own, or small
	// under a player's name, where class hides it from the width the column shows.
	let { id, small = false, class: klass = '' }: { id: string; small?: boolean; class?: string } = $props();
</script>

<a class="{small ? 'block font-mono text-[11px] text-faint' : 'font-mono text-xs text-muted'} hover:text-fg {klass}" href={steamProfile(id)} target="_blank" rel="noreferrer"
	>{id}</a
>

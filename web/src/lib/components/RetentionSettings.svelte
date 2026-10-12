<script lang="ts">
	import type { Retention } from '#lib/api.js';
	import { Setting } from '#lib/setting.svelte.js';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	let auditDays = $state(0);
	let playerDays = $state(0);

	const setting = new Setting<Retention>('/manager/retention', (r) => {
		auditDays = r.auditDays;
		playerDays = r.playerDays;
	});
	const saved = $derived(setting.saved);

	const valid = (d: number) => Number.isInteger(d) && d >= 0 && d <= 3650;
	const changed = $derived(!!saved && (auditDays !== saved.auditDays || playerDays !== saved.playerDays));

	function save(e: SubmitEvent) {
		e.preventDefault();
		setting.save({ auditDays, playerDays });
	}

	guardUnsaved(() => changed);
</script>

{#if !saved}
	{#if setting.error}<LoadError error={setting.error} onretry={setting.load} />{:else}<Loading />{/if}
{:else}
	<form class="space-y-4" onsubmit={save}>
		<p class="text-sm text-muted">So the manager's database doesn't grow forever, older records are deleted every hour. 0 keeps them for good.</p>
		<div class="grid gap-3 sm:grid-cols-2">
			<label class="block">
				<span class="label">Keep the audit log for</span>
				<span class="flex items-center gap-2 text-sm">
					<input class="input w-24 tabular-nums" type="number" min="0" max="3650" bind:value={auditDays} /> days
				</span>
			</label>
			<label class="block">
				<span class="label">Forget players not seen for</span>
				<span class="flex items-center gap-2 text-sm">
					<input class="input w-24 tabular-nums" type="number" min="0" max="3650" bind:value={playerDays} /> days
				</span>
			</label>
		</div>
		<p class="text-xs text-faint">
			Performance charts keep 7 days, and downloaded server releases in <code>cache/</code> keep the newest two.
		</p>
		<button class="btn btn-primary" disabled={!changed || setting.busy || !valid(auditDays) || !valid(playerDays)}>Save</button>
	</form>
{/if}

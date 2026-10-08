<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message, type Retention } from '#lib/api.js';
	import { attempt } from '#lib/toast.svelte.js';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	let saved = $state.raw<Retention | null>(null);
	let auditDays = $state(0);
	let playerDays = $state(0);
	let busy = $state(false);

	function show(r: Retention) {
		saved = r;
		auditDays = r.auditDays;
		playerDays = r.playerDays;
	}
	let loadError = $state('');
	async function load() {
		loadError = '';
		try {
			show(await api.get<Retention>('/manager/retention'));
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(load);

	const valid = (d: number) => Number.isInteger(d) && d >= 0 && d <= 3650;
	const changed = $derived(!!saved && (auditDays !== saved.auditDays || playerDays !== saved.playerDays));

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		const r = await attempt(() => api.patch<Retention>('/manager/retention', { auditDays, playerDays }), 'Saved');
		busy = false;
		if (r) show(r);
	}

	guardUnsaved(() => changed);
</script>

{#if !saved}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
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
		<button class="btn btn-primary" disabled={!changed || busy || !valid(auditDays) || !valid(playerDays)}>Save</button>
	</form>
{/if}

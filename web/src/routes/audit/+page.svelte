<script lang="ts">
	import { untrack } from 'svelte';
	import { api, message, type AuditEntry } from '#lib/api.js';
	import { instances } from '#lib/instances.svelte.js';
	import LoadError from '#lib/components/LoadError.svelte';
	import { newest } from '#lib/newest.js';
	import { dateTime } from '#lib/format.js';

	let rows = $state.raw<AuditEntry[]>([]);
	let instance = $state('');
	let more = $state(false);
	let loading = $state(false);
	let loaded = $state(false);
	let error = $state('');

	const begin = newest();
	async function load(reset = true) {
		const current = begin();
		const before = reset ? 0 : (rows.at(-1)?.id ?? 0);
		loading = true;
		try {
			const page = await api.get<AuditEntry[]>(`/audit?instance=${encodeURIComponent(instance)}&before=${before}`);
			if (!current()) return; // the filter changed meanwhile
			// Entries written since the last page shift nothing: pages go by id.
			const seen = new Set(reset ? [] : rows.map((r) => r.id));
			rows = reset ? page : [...rows, ...page.filter((r) => !seen.has(r.id))];
			more = page.length === 100;
			loaded = true;
			error = '';
		} catch (e) {
			// What was shown stays, and so does Load older.
			if (current()) error = message(e);
		} finally {
			if (current()) loading = false;
		}
	}
	$effect(() => {
		instance;
		untrack(load);
	});

	const tone = (a: string) =>
		a.includes('failed') ? 'text-bad' : a.startsWith('player.') ? 'text-warn' : a.startsWith('server.') ? 'text-info' : a.startsWith('auth.') ? 'text-muted' : 'text-fg';
</script>

<div class="mx-auto max-w-6xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div class="flex flex-wrap items-end justify-between gap-4">
		<div>
			<h1 class="page-title">Audit log</h1>
			<p class="mt-2 text-sm text-muted">Every action taken through the panel, newest first.</p>
		</div>
		<select class="input w-full sm:w-56" aria-label="Server" bind:value={instance}>
			<option value="">All servers and panel</option>
			{#each instances.list as i (i.id)}<option value={i.id}>{i.name}</option>{/each}
		</select>
	</div>
	{#if error}<LoadError {error} onretry={() => load(!rows.length)} />{/if}
	<div class="card overflow-x-auto">
		<table class="table">
			<thead>
				<tr>
					<th>When</th><th>Who</th><th class="hidden sm:table-cell">Server</th><th>Action</th><th class="hidden md:table-cell">Detail</th><th class="hidden lg:table-cell">IP</th>
				</tr>
			</thead>
			<tbody>
				{#each rows as r (r.id)}
					<tr>
						<td class="text-muted sm:whitespace-nowrap">{dateTime(r.at)}</td>
						<td>{r.username || '—'}</td>
						<td class="hidden text-muted sm:table-cell">{r.instance ? (instances.byId(r.instance)?.name ?? r.instance) : '—'}</td>
						<td class="font-mono text-xs {tone(r.action)}">
							{r.action}
							<!-- The detail column is hidden on phones, so it rides under the action. -->
							{#if r.detail}<div class="max-w-48 truncate font-sans text-muted md:hidden" title={r.detail}>{r.detail}</div>{/if}
						</td>
						<td class="hidden max-w-md truncate text-xs text-muted md:table-cell" title={r.detail}>{r.detail || ''}</td>
						<td class="hidden font-mono text-xs text-faint lg:table-cell">{r.ip || ''}</td>
					</tr>
				{:else}
					<tr><td colspan="6" class="py-6 text-center text-faint">{loading ? 'loading…' : loaded ? 'Nothing logged yet.' : ''}</td></tr>
				{/each}
			</tbody>
		</table>
	</div>
	{#if more && rows.length}<button class="btn" disabled={loading} onclick={() => load(false)}>{loading ? 'Loading…' : 'Load older'}</button>{/if}
</div>

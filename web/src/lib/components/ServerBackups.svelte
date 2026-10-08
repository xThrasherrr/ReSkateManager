<script lang="ts">
	import { api, followJob, message, type InstanceView, type ServerBackup } from '#lib/api.js';
	import { instances } from '#lib/instances.svelte.js';
	import { session } from '#lib/session.svelte.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { backupLabel, bytes, dateTime } from '#lib/format.js';
	import Icon from './Icon.svelte';
	import Modal from './Modal.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import Progress from './Progress.svelte';

	let { st }: { st: InstanceView } = $props();

	let list = $state.raw<ServerBackup[] | null>(null);
	let error = $state('');
	async function load(id: string) {
		try {
			list = await api.get<ServerBackup[]>(`/instances/${id}/backups`);
			error = '';
		} catch (e) {
			error = message(e);
		}
	}
	const id = $derived(st.id);
	$effect(() => {
		load(id);
	});

	const stopped = $derived(st.state === 'stopped' || st.state === 'crashed');
	const canInstall = $derived(session.can('server.update', st.id));

	let picked = $state.raw<ServerBackup | null>(null);
	let confirming = $state(false);
	let withMods = $state(false);
	let install = $state(false);
	function pick(b: ServerBackup) {
		picked = b;
		withMods = b.modFiles;
		install = !st.installed && canInstall;
		confirming = true;
	}

	let job = $state<{ done: number; total: number } | null>(null);
	async function restore() {
		const b = picked!;
		confirming = false;
		job = { done: 0, total: 0 };
		const r = await attempt(
			async () => {
				const { id } = await api.post<{ id: string }>(`/instances/${st.id}/restore`, { backup: b.name, mods: withMods });
				return followJob<{ files: string[]; mods?: number; shared?: string[] }>(id, (j) => (job = { done: j.done, total: j.total }));
			},
			(r) => {
				const parts = [r.files.length ? r.files.join(' and ') : 'nothing in the config'];
				if (r.mods !== undefined) parts.push(`${r.mods} mod${r.mods === 1 ? '' : 's'}`);
				if (r.shared?.length) parts.push(`${r.shared.length} missing shared mod${r.shared.length === 1 ? '' : 's'}`);
				return `Restored ${parts.join(', ')}`;
			}
		);
		job = null;
		if (r && install) await attempt(() => api.post(`/instances/${st.id}/update`, { mode: 'now' }), 'Installing the server program…');
		instances.refresh();
	}

	let exportMods = $state(true);
</script>

<section class="card space-y-3 p-4 sm:p-5">
	<div>
		<h2>Backups</h2>
		<p class="mt-1 text-sm text-muted">
			Put this server's config and world layers back as they were, and its mods if the backup holds them. Its own mods are swapped for the backup's; ones
			from the shared mods stay.
		</p>
	</div>
	{#if job}
		<Progress value={job.total ? job.done / job.total : null} label="Restoring the backup" />
	{/if}
	{#if error}
		<LoadError {error} onretry={() => load(id)} />
	{/if}
	{#if !list}
		{#if !error}<Loading />{/if}
	{:else if list.length === 0}
		<p class="text-sm text-faint">No backup holds this server yet.{session.user?.owner ? ' Make one on the Manager page.' : ''}</p>
	{:else}
		{#if !stopped}<p class="text-xs text-warn">Stop the server to restore it.</p>{/if}
		<ul class="max-h-80 divide-y-2 divide-line overflow-y-auto rounded-xl border-2 border-line">
			{#each list as b (b.name)}
				<li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
					<div class="min-w-0 flex-1">
						<div class="text-sm">
							<span class="font-semibold">{dateTime(b.created)}</span>
							<span class="text-muted">· {backupLabel(b.kind, b.by)}</span>
						</div>
						<div class="flex flex-wrap items-center gap-x-2 text-xs text-faint">
							<span>{b.files.length ? b.files.join(', ') : 'No config'}</span>
							<span>· {b.mods.length} mod{b.mods.length === 1 ? '' : 's'}{b.modFiles ? '' : ', listed only'}</span>
							{#if b.modFiles}<span class="rounded bg-accent/15 px-1.5 text-accent">with mods</span>{/if}
							<span class="tabular-nums">{bytes(b.size)}</span>
						</div>
					</div>
					<button class="btn btn-sm" disabled={!stopped || !!job} onclick={() => pick(b)}><Icon name="restart" size={13} /> Restore</button>
				</li>
			{/each}
		</ul>
	{/if}
</section>

<section class="card space-y-3 p-4 sm:p-5">
	<div>
		<h2>Move to another manager</h2>
		<p class="mt-1 text-sm text-muted">
			Download this server as a zip: its config, world layers, panel settings and announcements, and its mods. Shared mods it loads come as its own. On the
			other manager, choose <span class="font-semibold">New server</span>, then <span class="font-semibold">From an export</span>.
		</p>
	</div>
	<div class="flex flex-wrap items-center gap-3">
		<a class="btn" href="/api/instances/{st.id}/export?mods={exportMods ? 1 : 0}" download><Icon name="download" size={14} /> Export</a>
		<label class="flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={exportMods} /> With mods</label>
	</div>
	<p class="text-xs text-faint">The server program isn't included; install it on the other manager from the server's Updates page.</p>
</section>

<Modal bind:open={confirming} title="Restore {st.name}?">
	{#if picked}
		<p class="text-sm text-muted">
			Its config{picked.files.includes('world-layers.json') ? ' and world layers' : ''} go back to how they were on {dateTime(picked.created)}. Changes made since
			are lost.
		</p>
		<div class="mt-4 space-y-2">
			<label class="flex items-start gap-2 text-sm {picked.modFiles ? '' : 'opacity-60'}">
				<input type="checkbox" class="mt-0.5" bind:checked={withMods} disabled={!picked.modFiles} />
				<span>
					Its mods too ({picked.mods.filter((m) => !m.shared).length})
					<span class="block text-xs text-faint">
						{#if picked.modFiles}Its own mods are replaced with the backup's. Shared mods missing from the manager come back too.{:else}This backup lists its
							mods but doesn't hold their files.{/if}
					</span>
				</span>
			</label>
			{#if canInstall && !st.installed}
				<label class="flex items-start gap-2 text-sm">
					<input type="checkbox" class="mt-0.5" bind:checked={install} />
					<span>Install the server program afterwards <span class="block text-xs text-faint">It isn't in backups, and this server has none.</span></span>
				</label>
			{/if}
		</div>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (confirming = false)}>Cancel</button>
		<button class="btn btn-primary" onclick={restore}>Restore</button>
	{/snippet}
</Modal>

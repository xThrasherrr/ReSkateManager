<script lang="ts">
	import { onMount } from 'svelte';
	import { api, followJob, message, type BackupInfo, type BackupSettings } from '#lib/api.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { backupLabel, bytes, dateTime } from '#lib/format.js';
	import Icon from './Icon.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import Modal from './Modal.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';
	import Progress from './Progress.svelte';

	let list = $state.raw<BackupInfo[] | null>(null);
	let folder = $state('');
	let saved = $state.raw<BackupSettings | null>(null);
	let scheduled = $state(true);
	let every = $state(24);
	let keep = $state(7);
	let mods = $state(false);
	let busy = $state(false);

	function show(s: BackupSettings) {
		saved = s;
		scheduled = s.every > 0;
		every = s.every || 24;
		keep = s.keep;
		mods = s.mods;
		nowMods = s.mods;
	}
	let loadError = $state('');
	async function load() {
		try {
			const r = await api.get<{ backups: BackupInfo[]; settings: BackupSettings; folder: string }>('/backups');
			list = r.backups;
			folder = r.folder;
			if (!saved) show(r.settings);
			loadError = '';
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(load);

	const next = $derived({ every: scheduled ? every : 0, keep, mods });
	const changed = $derived(!!saved && (next.every !== saved.every || next.keep !== saved.keep || next.mods !== saved.mods));
	const valid = $derived(Number.isInteger(keep) && keep >= 1 && keep <= 100 && (!scheduled || (Number.isInteger(every) && every >= 1 && every <= 720)));

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		const s = await attempt(() => api.patch<BackupSettings>('/backups', next), 'Saved');
		busy = false;
		if (s) show(s);
	}

	// Back up now. With mods it can take minutes, so the manager does it as a job.
	let nowMods = $state(false);
	let job = $state<{ done: number; total: number } | null>(null);
	async function backUp() {
		job = { done: 0, total: 0 };
		const b = await attempt(
			async () => {
				const { id } = await api.post<{ id: string }>('/backups', { mods: nowMods });
				return followJob<BackupInfo>(id, (j) => (job = { done: j.done, total: j.total }));
			},
			(b) => `Backed up to ${b.name}`
		);
		job = null;
		if (b) load();
	}

	let deleting = $state.raw<BackupInfo | null>(null);
	let confirming = $state(false);
	async function remove() {
		const b = deleting!;
		confirming = false;
		if (await attempt(() => api.del(`/backups/${encodeURIComponent(b.name)}`), 'Deleted')) load();
	}

	function holds(b: BackupInfo) {
		const parts: string[] = [];
		if (b.database) parts.push('Database');
		const [only, ...more] = b.servers;
		if (only) parts.push(more.length ? `${b.servers.length} servers` : only.name);
		return parts.join(' + ') || 'Nothing';
	}
	const withMods = (b: BackupInfo) => b.sharedFiles || b.servers.some((s) => s.modFiles);

	guardUnsaved(() => changed);
</script>

{#if !saved}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
{:else}
	<div class="space-y-5">
		{#if loadError}<LoadError error={loadError} onretry={load} />{/if}
		<p class="text-sm text-muted">
			A backup holds the manager's database and settings, and each server's config and world layers. Server programs are left out, since they download
			again, and so are mods unless you include them: one map can be gigabytes.
		</p>

		<form class="space-y-3" onsubmit={save}>
			<label class="flex items-center gap-2 text-sm">
				<input type="checkbox" bind:checked={scheduled} />
				<span class="flex flex-wrap items-center gap-2">
					Back up every
					<input class="input w-20 tabular-nums" type="number" min="1" max="720" bind:value={every} disabled={!scheduled} aria-label="Hours between backups" />
					hours
				</span>
			</label>
			<label class="flex flex-wrap items-center gap-2 text-sm">
				Keep the newest
				<input class="input w-20 tabular-nums" type="number" min="1" max="100" bind:value={keep} aria-label="Backups to keep" />
				of each kind
			</label>
			<label class="flex items-start gap-2 text-sm">
				<input type="checkbox" class="mt-0.5" bind:checked={mods} disabled={!scheduled} />
				<span>
					Include mods in scheduled backups
					<span class="block text-xs text-faint">Each backup kept is a full copy of every mod, the shared ones too.</span>
				</span>
			</label>
			<p class="text-xs text-faint">
				The manager also backs up before it installs a server update (that server's config), before it updates itself, and before it upgrades its
				database. Backups you make here stay until you delete them.
			</p>
			<button class="btn btn-primary" disabled={!changed || busy || !valid}>Save</button>
		</form>

		<div class="space-y-2 border-t-2 border-line pt-4">
			<div class="flex flex-wrap items-center gap-3">
				<button class="btn" disabled={!!job} onclick={backUp}><Icon name="archive" size={14} /> {job ? 'Backing up…' : 'Back up now'}</button>
				<label class="flex items-center gap-2 text-sm">
					<input type="checkbox" bind:checked={nowMods} disabled={!!job} /> With mods
				</label>
			</div>
			{#if job}
				<Progress value={job.total ? job.done / job.total : null} label="Backing up" />
				{#if job.total}<p class="text-xs text-faint tabular-nums">{bytes(job.done)} of {bytes(job.total)}</p>{/if}
			{/if}
		</div>

		{#if list}
			{#if list.length === 0}
				<p class="text-sm text-faint">No backups yet.</p>
			{:else}
				<ul class="divide-y-2 divide-line rounded-xl border-2 border-line">
					{#each list as b (b.name)}
						<li class="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5">
							<div class="min-w-0 flex-1">
								{#if b.error}
									<div class="font-mono text-xs break-all">{b.name}</div>
									<div class="text-xs text-bad">Can't be read: {b.error}</div>
								{:else}
									<div class="text-sm">
										<span class="font-semibold">{dateTime(b.created)}</span>
										<span class="text-muted">· {backupLabel(b.kind, b.by)}</span>
									</div>
									<div class="flex flex-wrap items-center gap-x-2 text-xs text-faint">
										<span>{holds(b)}</span>
										{#if withMods(b)}<span class="rounded bg-accent/15 px-1.5 text-accent">with mods</span>{/if}
										<span class="tabular-nums">{bytes(b.size)}</span>
									</div>
								{/if}
							</div>
							<div class="flex shrink-0 gap-1">
								<a class="btn btn-ghost btn-sm" href="/api/backups/{encodeURIComponent(b.name)}" download title="Download {b.name}" aria-label="Download {b.name}"
									><Icon name="download" size={14} /></a
								>
								<button class="btn btn-ghost btn-sm" onclick={() => ((deleting = b), (confirming = true))} title="Delete" aria-label="Delete {b.name}"><Icon name="trash" size={14} /></button>
							</div>
						</li>
					{/each}
				</ul>
			{/if}
			<p class="text-xs text-faint">
				Kept in <code class="break-all">{folder}</code>; a backup on the same disk won't survive the disk, so download one now and then. To restore a server,
				open its Server page. To restore everything, stop the manager and run <code>ReSkateManager --restore &lt;backup&gt;</code>.
			</p>
		{/if}
	</div>
{/if}

<Modal bind:open={confirming} title="Delete this backup?">
	<p class="text-sm text-muted">
		<span class="font-mono text-xs break-all">{deleting?.name}</span> is deleted for good.
	</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (confirming = false)}>Cancel</button>
		<button class="btn btn-danger" onclick={remove}>Delete</button>
	{/snippet}
</Modal>

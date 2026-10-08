<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, followJob, type InstanceView } from '#lib/api.js';
	import { instances } from '#lib/instances.svelte.js';
	import { session } from '#lib/session.svelte.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import { sendChunks } from '#lib/uploads.svelte.js';
	import { duration, stateColor } from '#lib/format.js';
	import Icon from '#lib/components/Icon.svelte';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import { ask } from '#lib/ask.svelte.js';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';
	import { serverNameProblem } from '#lib/text.js';
	import Progress from '#lib/components/Progress.svelte';

	let creating = $state(false);
	let name = $state('');
	// A fresh install, a folder already on the host (owners only), or an export from another manager.
	let mode = $state<'install' | 'folder' | 'export'>('install');
	let dir = $state('');
	let busy = $state(false);

	let file = $state.raw<File | null>(null);
	let installAfter = $state(true);
	let importing = $state<{ phase: 'uploading' | 'importing'; part: number } | null>(null);

	async function create(e: SubmitEvent) {
		e.preventDefault();
		if (mode === 'export') return importExport();
		busy = true;
		const inst = await attempt(
			() => api.post<InstanceView>('/instances', { name, dir: mode === 'folder' ? dir : '', install: mode === 'install' }),
			(v) => (mode === 'install' ? `Created ${v.name}; installing the latest server…` : `Added ${v.name}`)
		);
		busy = false;
		if (inst) {
			creating = false;
			name = dir = '';
			await instances.refresh();
			goto(`/s/${inst.id}`);
		}
	}

	// The export goes up in chunks like a mod, then the manager unpacks it as a job.
	let importer: AbortController | null = null;
	let detached = false; // the dialog closed while the manager imports: say so when it's in, and stay put
	async function importExport() {
		const f = file!;
		const ctl = (importer = new AbortController());
		detached = false;
		busy = true;
		importing = { phase: 'uploading', part: 0 };
		const r = await attempt(
			async () => {
				const { id } = await sendChunks<{ id: string }>(
					'/imports',
					f,
					(sent, all) => (importing = { phase: all ? 'importing' : 'uploading', part: sent / f.size }),
					ctl.signal
				);
				importing = { phase: 'importing', part: 0 };
				return followJob<{ id: string; name: string; notes?: string[] }>(id, (j) => (importing = { phase: 'importing', part: j.total ? j.done / j.total : 0 }));
			},
			(v) => `Imported ${v.name}`
		);
		busy = false;
		importing = null;
		importer = null;
		if (!r) return;
		for (const note of r.notes ?? []) if (!(installAfter && note.includes('Updates page'))) toast.info(note);
		if (installAfter) await attempt(() => api.post(`/instances/${r.id}/update`, { mode: 'now' }), 'Installing the server program…');
		await instances.refresh();
		if (detached) return;
		creating = false;
		file = null;
		goto(`/s/${r.id}`);
	}

	// Closing the dialog, or leaving the page, stops an upload under way. Once
	// the manager has the whole file, the import carries on without them.
	function leaveImport() {
		if (!importing || detached) return;
		file = null; // the dialog's file input is empty when it opens again
		if (importing.phase === 'uploading') {
			importer?.abort();
			toast.info('Import cancelled');
		} else {
			detached = true;
			toast.info('The manager carries on with the import; the server shows up here once it is in.');
		}
	}
	$effect(() => {
		if (!creating) untrack(leaveImport);
	});
	$effect(() => () => leaveImport());

	// The server whose start or stop is on its way, so its button waits for it.
	let asking = $state('');
	async function lifecycle(inst: InstanceView, action: 'start' | 'stop') {
		if (asking) return;
		const n = inst.state === 'running' ? inst.players : 0;
		if (action === 'stop' && n > 0 && !(await ask({ title: `Stop ${inst.name}?`, body: `${`${n} player${n === 1 ? ' is' : 's are'} on`}. They are dropped.`, action: 'Stop' })))
			return;
		asking = inst.id;
		await attempt(() => api.post(`/instances/${inst.id}/${action}`));
		asking = '';
		instances.refresh();
	}

	let now = $state(Date.now());
	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});

	guardUnsaved(() => importing?.phase === 'uploading', 'The export is still uploading; leaving stops it.');

	const nameProblem = $derived(mode === 'export' ? '' : serverNameProblem(name));

	// Links elsewhere ("Add a server") open the dialog with /?new.
	$effect(() => {
		if (!page.url.searchParams.has('new')) return;
		untrack(() => {
			if (session.can('instances.manage')) creating = true;
			goto('/', { replace: true, shallow: true });
		});
	});
</script>

<div class="mx-auto max-w-6xl p-4 sm:p-6">
	<div class="mb-6 flex sm:mb-8 flex-wrap items-end justify-between gap-4">
		<div>
			<h1 class="page-title">Servers</h1>
			<p class="mt-2 text-sm text-muted">Every ReSkate server this manager runs.</p>
		</div>
		{#if session.can('instances.manage')}
			<button class="btn btn-primary" onclick={() => (creating = true)}><Icon name="plus" /> New server</button>
		{/if}
	</div>

	{#if instances.error}
		<p class="mb-4 rounded-xl border-2 border-warn/60 bg-warn/10 px-4 py-3 text-sm text-warn" role="status">
			{instances.loaded ? "Can't refresh the server list, so it may be out of date:" : "Can't load the servers:"}
			{instances.error} Trying again…
		</p>
	{/if}

	{#if instances.loaded && instances.list.length === 0}
		<div class="card grid place-items-center border-dashed px-6 py-16 text-center">
			<span class="mb-4 grid size-16 -rotate-6 place-items-center rounded-2xl border-2 border-ink bg-accent text-ink shadow-sticker"><Icon name="server" size={30} /></span>
			<h2 class="text-2xl">No servers yet</h2>
			<p class="mt-1 max-w-sm text-sm text-muted">
				{#if session.can('instances.manage')}
					Create one and the manager downloads the latest ReSkate server release into its own folder, or add a folder you already have.
				{:else}
					None that you can see, anyway. An owner, or someone who manages users, can give you access to one.
				{/if}
			</p>
			{#if session.can('instances.manage')}
				<button class="btn btn-primary mt-4" onclick={() => (creating = true)}><Icon name="plus" /> New server</button>
			{/if}
		</div>
	{/if}

	<div class="grid gap-4 sm:gap-5 md:grid-cols-2 xl:grid-cols-3">
		{#each instances.list as inst (inst.id)}
			{@const canLife = inst.permissions?.includes('server.lifecycle')}
			{@const moving = inst.state === 'starting' || inst.state === 'stopping' || inst.state === 'updating'}
			<div class="card flex flex-col overflow-hidden transition-[translate,box-shadow] hover:-translate-y-0.5 hover:shadow-sticker-lg">
				<div class="h-2.5 border-b-2 border-ink {stateColor[inst.state]} {moving ? 'tape-stripes animate-tape' : ''}"></div>
				<a href="/s/{inst.id}" class="block flex-1 p-4">
					<div class="mb-4 flex items-start justify-between gap-2">
						<div class="min-w-0">
							<h2 class="truncate text-xl leading-tight">{inst.name}</h2>
							<div class="truncate text-xs text-faint">{inst.info.serverName ?? inst.id}</div>
						</div>
						<StateBadge state={inst.state} />
					</div>
					<dl class="grid grid-cols-3 gap-x-2 gap-y-3 text-sm">
						<div>
							<dt class="text-[10px] font-semibold tracking-wider text-faint uppercase">Players</dt>
							<dd class="font-display text-lg leading-tight">{inst.state === 'running' ? `${inst.players}/${inst.info.maxPlayers ?? '?'}` : '—'}</dd>
						</div>
						<div class="col-span-2 min-w-0">
							<dt class="text-[10px] font-semibold tracking-wider text-faint uppercase">Map</dt>
							<dd class="truncate font-display text-lg leading-tight">{inst.state === 'running' ? (inst.info.map ?? '—') : '—'}</dd>
						</div>
						<div>
							<dt class="text-[10px] font-semibold tracking-wider text-faint uppercase">Uptime</dt>
							<dd class="tabular-nums">{inst.state === 'running' ? duration(inst.readyAt, now) : '—'}</dd>
						</div>
						<div class="col-span-2 min-w-0">
							<dt class="text-[10px] font-semibold tracking-wider text-faint uppercase">Status</dt>
							<dd class="truncate text-muted">
								{#if !inst.installed}Not installed{:else if inst.state === 'crashed'}{inst.lastError || `Exited (${inst.exitCode})`}{:else if inst.info.password}Password protected{:else}Open{/if}
							</dd>
						</div>
					</dl>
				</a>
				{#if canLife}
					<div class="flex gap-2 border-t-2 border-line bg-sunken/40 p-3">
						{#if inst.state === 'running' || inst.state === 'starting'}
							<button class="btn btn-sm" disabled={asking === inst.id} onclick={() => lifecycle(inst, 'stop')}><Icon name="stop" size={14} /> Stop</button>
						{:else}
							<button class="btn btn-sm" disabled={asking === inst.id || !inst.installed || inst.state === 'stopping' || inst.state === 'updating'} onclick={() => lifecycle(inst, 'start')}
								><Icon name="play" size={14} /> Start</button
							>
						{/if}
						<a href="/s/{inst.id}" class="btn btn-sm btn-ghost ml-auto"><Icon name="terminal" size={14} /> Console</a>
					</div>
				{/if}
			</div>
		{/each}
	</div>
</div>

<Modal bind:open={creating} title="New server" wide={!!session.perms?.owner}>
	<form id="create" onsubmit={create} class="space-y-4">
		{#if mode !== 'export'}
			<div>
				<label class="label" for="n">Name</label>
				<input id="n" class="input" bind:value={name} required placeholder="My ReSkate server" aria-invalid={!!name && !!nameProblem} />
				{#if name && nameProblem}<p class="mt-1 text-xs text-bad">{nameProblem}</p>{/if}
				<p class="mt-1 text-xs text-faint">Also used as the server's name in the browser; you can change either later.</p>
			</div>
		{/if}
		<!-- A folder elsewhere on the host is an owner's choice; the API refuses it to anyone else. -->
		<fieldset>
			<legend class="label">Where it comes from</legend>
			<div class="grid gap-2 text-sm {session.perms?.owner ? 'sm:grid-cols-3' : 'sm:grid-cols-2'}">
				<label class="flex cursor-pointer items-start gap-2 rounded-xl border-2 p-3 transition-colors {mode === 'install' ? 'border-accent bg-accent/10' : 'border-line-strong hover:border-muted'}">
					<input type="radio" bind:group={mode} value="install" class="mt-0.5" disabled={busy} />
					<span><span class="font-medium">Install fresh</span><br /><span class="text-xs text-muted">Download the latest release into a new folder.</span></span>
				</label>
				{#if session.perms?.owner}
					<label class="flex cursor-pointer items-start gap-2 rounded-xl border-2 p-3 transition-colors {mode === 'folder' ? 'border-accent bg-accent/10' : 'border-line-strong hover:border-muted'}">
						<input type="radio" bind:group={mode} value="folder" class="mt-0.5" disabled={busy} />
						<span><span class="font-medium">Existing folder</span><br /><span class="text-xs text-muted">Manage a server you already set up.</span></span>
					</label>
				{/if}
				<label class="flex cursor-pointer items-start gap-2 rounded-xl border-2 p-3 transition-colors {mode === 'export' ? 'border-accent bg-accent/10' : 'border-line-strong hover:border-muted'}">
					<input type="radio" bind:group={mode} value="export" class="mt-0.5" disabled={busy} />
					<span><span class="font-medium">From an export</span><br /><span class="text-xs text-muted">A server exported from another manager.</span></span>
				</label>
			</div>
		</fieldset>
		{#if mode === 'export'}
			<div>
				<label class="label" for="z">Export zip</label>
				<input id="z" class="input" type="file" accept=".zip,application/zip" required disabled={busy} onchange={(e) => (file = e.currentTarget.files?.[0] ?? null)} />
				<p class="mt-1 text-xs text-faint">
					From the other manager's server page, under Server, Export. It keeps its name, settings, announcements and mods, in a new folder here.
				</p>
			</div>
			<label class="flex items-start gap-2 text-sm">
				<input type="checkbox" class="mt-0.5" bind:checked={installAfter} disabled={busy} />
				<span>Install the latest server program <span class="block text-xs text-faint">Exports don't hold it.</span></span>
			</label>
			{#if importing}
				<div class="space-y-1">
					<Progress
						value={importing.phase === 'importing' && !importing.part ? null : importing.part}
						label={importing.phase === 'uploading' ? 'Uploading the export' : 'Importing the server'}
					/>
					<p class="text-xs text-faint">{importing.phase === 'uploading' ? `Uploading ${Math.round(importing.part * 100)}%` : 'Unpacking…'}</p>
				</div>
			{/if}
		{/if}
		{#if mode === 'folder'}
			<div>
				<label class="label" for="d">Server folder</label>
				<input id="d" class="input font-mono" bind:value={dir} required placeholder="C:\ReSkateServer" />
				<p class="mt-1 text-xs text-faint">The folder with ReSkateServer.exe and ReSkateServer.json. Its config and Mods are kept as they are.</p>
			</div>
		{/if}
	</form>
	{#snippet actions()}
		<button class="btn" onclick={() => (creating = false)}>Cancel</button>
		{#if mode === 'export'}
			<button class="btn btn-primary" form="create" disabled={busy || !file}>{busy ? 'Importing…' : 'Import'}</button>
		{:else}
			<button class="btn btn-primary" form="create" disabled={busy || !!nameProblem}>{busy ? 'Creating…' : 'Create'}</button>
		{/if}
	{/snippet}
</Modal>

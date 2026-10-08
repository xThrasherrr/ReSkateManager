<script lang="ts">
	import { untrack } from 'svelte';
	import { api, message, type UpdateStatus } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt, flip, toast } from '#lib/toast.svelte.js';
	import { ago, bytes } from '#lib/format.js';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import Progress from '#lib/components/Progress.svelte';

	const ctx = useLive();
	let st = $state.raw<UpdateStatus | null>(null);
	let checking = $state(false);
	const busy = $derived(!!st?.job && !st.job.finished);

	let loadError = $state('');
	async function load(refresh = false) {
		checking = refresh;
		try {
			st = await api.get<UpdateStatus>(`/instances/${ctx.id}/update${refresh ? '?refresh=1' : ''}`);
			loadError = '';
		} catch (e) {
			// Before the first answer the page says why; after it, a Check now
			// that failed says so, and a missed poll waits for the next.
			if (!st) loadError = message(e);
			else if (refresh) toast.error(e);
		}
		checking = false;
	}
	$effect(() => {
		ctx.id;
		untrack(load);
	});
	// Poll while a job runs. On busy, not st, so each answer keeps the same timer.
	$effect(() => {
		if (!busy) return;
		const t = setInterval(() => load(), 1500);
		return () => clearInterval(t);
	});

	let starting = $state(false);
	async function start(mode: 'now' | 'empty') {
		if (starting) return;
		starting = true;
		const r = await attempt(() => api.post<UpdateStatus>(`/instances/${ctx.id}/update`, { mode }));
		starting = false;
		if (r) st = r;
	}

	function toggleAuto(on: boolean) {
		return attempt(() => api.patch(`/instances/${ctx.id}`, { autoUpdate: on }), on ? 'Auto-update on' : 'Auto-update off');
	}

	const running = $derived(ctx.live.state?.state === 'running');
	const players = $derived(ctx.live.players.length);
	const pct = $derived(st?.job?.total ? Math.round(((st.job.done ?? 0) / st.job.total) * 100) : 0);
</script>

<div class="mx-auto max-w-3xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	{#if st}
		<section class="card p-4 sm:p-5">
			<div class="flex flex-wrap items-start gap-4">
				<div class="min-w-0 flex-1">
					<h2>ReSkate server</h2>
					{#if !st.supported}
						<p class="mt-1 text-sm text-muted">{st.latestError}</p>
					{:else if st.latestError}
						<p class="mt-1 text-sm text-warn">Could not check for updates: {st.latestError}</p>
					{:else if !st.installed}
						<p class="mt-1 text-sm text-warn">The server is not installed in this folder yet.</p>
					{:else if st.upToDate}
						<p class="mt-1 flex items-center gap-1.5 text-sm text-ok"><Icon name="check" size={14} /> Up to date with {st.latest?.version}</p>
					{:else}
						<p class="mt-1.5"><span class="sticker sticker-accent">New</span> <span class="ml-1 text-sm text-accent">Server {st.latest?.version} is available.</span></p>
					{/if}
					{#if st.installed && !st.upToDate}
						<p class="mt-1 text-sm text-muted">
							{#if st.version}This server runs {st.version}.{:else}The manager can't tell which release this is: it knows the ones it has looked up or installed.{/if}
						</p>
					{/if}
					{#if st.latest}
						<p class="mt-1 text-xs text-faint">
							Latest release {st.latest.tag || st.latest.version} · {bytes(st.latest.size)} · checked {ago(st.latest.checkedAt)}
						</p>
					{/if}
				</div>
				{#if st.supported}
					<button class="btn" disabled={checking} onclick={() => load(true)}><Icon name="restart" size={14} /> {checking ? 'Checking…' : 'Check now'}</button>
				{/if}
			</div>

			{#if st.supported && st.latest && (!st.upToDate || !st.installed) && !busy}
				<div class="mt-4 flex flex-wrap gap-2 border-t-2 border-line pt-4">
					{#if running && players > 0}
						<button class="btn btn-primary" disabled={starting} onclick={() => start('empty')}><Icon name="download" size={14} /> Install when the server is empty</button>
						<button class="btn h-auto min-h-8 py-1 text-left" disabled={starting} onclick={() => start('now')}>Install now (warns {players} player{players === 1 ? '' : 's'}, restarts in 60 s)</button>
					{:else}
						<button class="btn btn-primary" disabled={starting} onclick={() => start('now')}>
							<Icon name="download" size={14} />
							{st.installed ? `Install ${st.latest.version}${running ? ' and restart' : ''}` : `Install ${st.latest.version}`}
						</button>
					{/if}
				</div>
			{/if}

			{#if st.job}
				<div class="mt-4 border-t-2 border-line pt-4 text-sm">
					{#if st.job.phase === 'downloading'}
						<div class="mb-1 flex justify-between text-muted"><span>Downloading {st.job.version}…</span><span class="tabular-nums">{pct}%</span></div>
						<Progress value={pct / 100} stripes size="lg" label="Downloading server {st.job.version}" />
					{:else if st.job.phase === 'waiting'}
						<p class="text-muted">Downloaded {st.job.version}; waiting for players to leave before installing.</p>
					{:else if st.job.phase === 'installing'}
						<p class="text-muted">Installing {st.job.version}…</p>
					{:else if st.job.phase === 'done'}
						<p class="text-ok">Installed {st.job.version} {ago(st.job.finished)}{st.job.by ? ` by ${st.job.by}` : ''}.</p>
					{:else if st.job.phase === 'failed'}
						<p class="text-bad">Update failed: {st.job.error}</p>
					{/if}
				</div>
			{/if}
		</section>

		{#if st.supported && ctx.can('instances.manage')}
			<section class="card flex items-center gap-4 p-4 sm:p-5">
				<div class="flex-1">
					<h2 id="auto-updates">Automatic updates</h2>
					<p class="text-sm text-muted">Check every 30 minutes and install new releases once nobody is on.</p>
				</div>
				<label class="inline-flex cursor-pointer items-center">
					<input
						type="checkbox"
						aria-labelledby="auto-updates"
						class="peer sr-only"
						checked={ctx.live.state?.autoUpdate}
						onchange={(e) => flip(e, toggleAuto)}
					/>
					<span class="switch"></span>
				</label>
			</section>
		{/if}

		<section class="card p-4 text-sm text-muted sm:p-5">
			<h2 class="mb-1 text-fg">How updates work</h2>
			The manager reads <span class="font-mono text-xs">launcher.json</span> from the latest ReSkate release, downloads the server zip, checks it against the
			published SHA-256 and replaces the server's files. <span class="font-mono text-xs">ReSkateServer.json</span>, <span class="font-mono text-xs">Mods/</span>,
			<span class="font-mono text-xs">world-layers.json</span> and logs are never touched. The server's own self-updater is switched off so it never restarts
			outside the manager's control.
		</section>
	{:else if loadError}
		<LoadError error={loadError} onretry={() => load()} />
	{:else}
		<Loading />
	{/if}
</div>

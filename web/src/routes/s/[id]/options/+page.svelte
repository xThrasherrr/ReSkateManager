<script lang="ts">
	import { goto } from '$app/navigation';
	import { api } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { instances } from '#lib/instances.svelte.js';
	import { attempt, flip } from '#lib/toast.svelte.js';
	import { session } from '#lib/session.svelte.js';
	import Modal from '#lib/components/Modal.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import ServerBackups from '#lib/components/ServerBackups.svelte';
	import { Busy } from '#lib/busy.svelte.js';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';
	import { serverNameProblem } from '#lib/text.js';

	const ctx = useLive();
	const st = $derived(ctx.live.state);
	// The input edits name. It derives from the name string, not st, so it resets
	// only when the name really changes, not on every state push mid-typing.
	const savedName = $derived(st?.name ?? '');
	let name = $derived(savedName);

	const busy = new Busy();
	async function patch(body: Record<string, unknown>, msg: string, key: string) {
		const r = await busy.run(key, () => attempt(() => api.patch(`/instances/${ctx.id}`, body), msg));
		if (r) instances.refresh();
		return r;
	}

	let showIp = $state(false);

	let confirmDelete = $state(false);
	let purge = $state(false);
	async function remove() {
		confirmDelete = false;
		const r = await attempt(
			() => api.del<{ filesKept?: string; filesDeleted?: string }>(`/instances/${ctx.id}${purge ? '?purge=1' : ''}`),
			(r) => (r.filesDeleted ? `Removed, and deleted ${r.filesDeleted}` : `Removed. Its files are still in ${r.filesKept}`)
		);
		if (r) {
			await instances.refresh();
			goto('/');
		}
	}

	const toggles = [
		{ key: 'autoStart', label: 'Start with the manager', help: 'Start this server whenever ReSkateManager starts.' },
		{ key: 'autoRestart', label: 'Restart after a crash', help: 'Up to five tries in a row, waiting longer each time. Config errors are not retried.' }
	] as const;

	// Scheduled restarts. Like the name, the edits derive from strings of what is
	// saved, so a state push mid-edit doesn't reset them.
	type Mode = 'off' | 'times' | 'hours';
	const savedTimes = $derived((st?.restartTimes ?? []).join(','));
	const savedHours = $derived(st?.restartHours ?? 0);
	let mode = $derived<Mode>(savedTimes ? 'times' : savedHours ? 'hours' : 'off');
	let times = $derived(savedTimes ? savedTimes.split(',') : ['04:00']);
	let hours = $derived(savedHours || 12);
	const schedule = $derived({
		restartTimes: mode === 'times' ? times.filter(Boolean) : [],
		restartHours: mode === 'hours' ? hours : 0
	});
	const scheduleChanged = $derived(schedule.restartTimes.join(',') !== savedTimes || schedule.restartHours !== savedHours);
	const modes: { value: Mode; label: string }[] = [
		{ value: 'off', label: 'Off' },
		{ value: 'times', label: 'At set times' },
		{ value: 'hours', label: 'Every few hours' }
	];

	let now = $state(Date.now());
	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 30_000);
		return () => clearInterval(t);
	});
	function until(ms: number) {
		const mins = Math.max(0, Math.round((ms - now) / 60_000));
		if (mins < 60) return `in ${mins} min`;
		const h = Math.floor(mins / 60);
		return h < 48 ? `in ${h} h ${mins % 60} min` : `in ${Math.floor(h / 24)} days`;
	}

	guardUnsaved(() => name !== savedName || scheduleChanged);

	const nameProblem = $derived(serverNameProblem(name));
</script>

{#if st}
	<div class="mx-auto max-w-3xl space-y-4 p-4 sm:space-y-6 sm:p-6">
		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">Panel name</h2>
			<form
				class="flex gap-2"
				onsubmit={(e) => {
					e.preventDefault();
					patch({ name }, 'Renamed', 'name');
				}}
			>
				<input class="input" bind:value={name} required aria-label="Panel name" aria-invalid={!!nameProblem} />
				<button class="btn" disabled={name.trim() === st.name || !!nameProblem || busy.is()}>{busy.is('name') ? 'Renaming…' : 'Rename'}</button>
			</form>
			{#if nameProblem}<p class="mt-2 text-xs text-bad">{nameProblem}</p>{/if}
			<p class="mt-2 text-xs text-faint">How this server is labelled in the panel. The name players see is under Settings.</p>
		</section>

		<section class="card divide-y-2 divide-line">
			{#each toggles as t (t.key)}
				<label class="flex cursor-pointer items-center gap-4 p-4 sm:p-5">
					<div class="flex-1">
						<div class="font-semibold">{t.label}</div>
						<div class="text-sm text-muted">{t.help}</div>
					</div>
					<input type="checkbox" class="peer sr-only" checked={st[t.key]} disabled={busy.is()} onchange={(e) => flip(e, (on) => patch({ [t.key]: on }, 'Saved', t.key))} />
					<span class="switch"></span>
				</label>
			{/each}
		</section>

		<section class="card space-y-3 p-4 sm:p-5">
			<div>
				<h2>Scheduled restarts</h2>
				<p class="mt-1 text-sm text-muted">
					Restart the server on a timer, such as each night. Players get a warning in chat 10, 5 and 1 minute before; with nobody on, it restarts
					straight away.
				</p>
			</div>
			<div class="seg" role="group" aria-label="Scheduled restarts">
				{#each modes as m (m.value)}
					<button class={mode === m.value ? 'seg-on' : ''} aria-pressed={mode === m.value} onclick={() => (mode = m.value)}>{m.label}</button>
				{/each}
			</div>
			{#if mode === 'times'}
				<div class="flex flex-wrap items-center gap-2">
					{#each times as t, i (i)}
						<span class="flex items-center gap-1">
							<input
								type="time"
								class="input w-auto tabular-nums"
								aria-label="Restart time {i + 1}"
								value={t}
								onchange={(e) => (times = times.with(i, e.currentTarget.value))}
							/>
							{#if times.length > 1}
								<button class="btn btn-ghost btn-sm size-7 px-0" aria-label="Remove {t}" onclick={() => (times = times.filter((_, j) => j !== i))}
									><Icon name="x" size={13} /></button
								>
							{/if}
						</span>
					{/each}
					{#if times.length < 6}
						<button class="btn btn-sm" onclick={() => (times = [...times, '16:00'])}><Icon name="plus" size={12} /> Add a time</button>
					{/if}
				</div>
				<p class="text-xs text-faint">On the manager's clock{session.meta?.timeZone ? `, ${session.meta.timeZone}` : ''}. A time within 10 minutes of the server starting is skipped.</p>
			{:else if mode === 'hours'}
				<label class="flex flex-wrap items-center gap-2 text-sm">
					After
					<input type="number" class="input w-20 tabular-nums" min="1" max="168" bind:value={hours} />
					hours up, counted from each start
				</label>
			{/if}
			<div class="flex flex-wrap items-center gap-3">
				<button class="btn btn-primary" disabled={!scheduleChanged || busy.is() || (mode === 'hours' && !(hours >= 1 && hours <= 168))} onclick={() => patch(schedule, 'Saved', 'schedule')}
					>Save</button
				>
				{#if st.nextRestart && !scheduleChanged}
					<span class="text-sm text-muted">
						Next restart {new Date(st.nextRestart).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' })}, {until(st.nextRestart)}
					</span>
				{:else if mode !== 'off' && !scheduleChanged && st.state !== 'running'}
					<span class="text-sm text-faint">Counts from the server's next start.</span>
				{/if}
			</div>
		</section>

		<section class="card p-4 text-sm sm:p-5">
			<h2 class="mb-3">Files</h2>
			<dl class="grid grid-cols-[5.5rem_minmax(0,1fr)] gap-x-2 gap-y-1.5 sm:grid-cols-[8rem_minmax(0,1fr)]">
				<dt class="text-faint">Folder</dt>
				<dd class="font-mono text-xs break-all">{st.dir}</dd>
				<dt class="text-faint">Server id</dt>
				<dd class="font-mono text-xs break-all">{st.id}</dd>
				{#if st.pid}<dt class="text-faint">Process</dt>
					<dd class="font-mono text-xs">PID {st.pid}</dd>{/if}
				{#if st.info.steamId}<dt class="text-faint">Steam ID</dt>
					<dd class="font-mono text-xs">{st.info.steamId}</dd>{/if}
				{#if st.info.publicIp}<dt class="text-faint">Public IP</dt>
					<dd>
						<button
							type="button"
							class="inline-flex cursor-pointer items-center gap-1.5 font-mono text-xs hover:text-muted"
							title={showIp ? 'Hide' : 'Click to show'}
							aria-label={showIp ? 'Hide public IP' : 'Show public IP'}
							onclick={() => (showIp = !showIp)}
							>{showIp ? st.info.publicIp : '•••.•••.•••.•••'}<Icon name={showIp ? 'eye-off' : 'eye'} size={13} class="text-faint" /></button
						>
					</dd>{/if}
			</dl>
		</section>

		<ServerBackups {st} />

		<section class="card border-bad/70 bg-[repeating-linear-gradient(-45deg,transparent_0_14px,color-mix(in_oklab,var(--color-bad)_6%,transparent)_14px_28px)] p-4 sm:p-5">
			<h2 class="text-bad">Remove from the manager</h2>
			<p class="mt-1 text-sm text-muted">The server must be stopped. Its folder, config and logs stay on disk unless you choose to delete them; you can add a kept folder back later. Its player history, announcements and performance samples go either way.</p>
			<button
				class="btn btn-danger mt-3"
				disabled={st.state !== 'stopped' && st.state !== 'crashed'}
				onclick={() => {
					purge = false;
					confirmDelete = true;
				}}
				><Icon name="trash" size={14} /> Remove server</button
			>
		</section>
	</div>
{/if}

<Modal bind:open={confirmDelete} title="Remove {st?.name}?">
	<p class="text-sm text-muted">
		Panel roles scoped to this server are removed too.
		{#if purge}
			Everything in <span class="font-mono text-xs break-all">{st?.dir}</span> is deleted. This cannot be undone.
		{:else}
			The files in <span class="font-mono text-xs break-all">{st?.dir}</span> are kept.
		{/if}
	</p>
	<label class="mt-4 flex cursor-pointer items-start gap-2 text-sm">
		<input type="checkbox" class="mt-0.5" bind:checked={purge} />
		<span><span class="font-semibold text-bad">Delete all data</span> — the server folder with its config, world, mods and logs</span>
	</label>
	{#snippet actions()}
		<button class="btn" onclick={() => (confirmDelete = false)}>Cancel</button>
		<button class="btn btn-danger" onclick={remove}>{purge ? 'Remove and delete' : 'Remove'}</button>
	{/snippet}
</Modal>

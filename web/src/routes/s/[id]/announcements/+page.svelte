<script lang="ts">
	import { untrack } from 'svelte';
	import { api, message as explain, type Announcement } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { attempt, flip } from '#lib/toast.svelte.js';
	import { newest } from '#lib/newest.js';
	import { Busy } from '#lib/busy.svelte.js';
	import Icon from '#lib/components/Icon.svelte';
	import InfoTip from '#lib/components/InfoTip.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import { ask } from '#lib/ask.svelte.js';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	const ctx = useLive();
	let list = $state.raw<Announcement[]>([]);
	let allServers = $state(false); // may manage announcements for every server
	let error = $state('');
	let loaded = $state(false);

	const begin = newest();
	async function load() {
		const current = begin();
		try {
			const r = await api.get<{ announcements: Announcement[]; allServers: boolean }>(`/instances/${ctx.id}/announcements`);
			if (!current()) return;
			list = r.announcements;
			allServers = r.allServers;
			error = '';
			loaded = true;
		} catch (e) {
			if (current()) error = explain(e);
		}
	}
	$effect(() => {
		ctx.id;
		untrack(load);
	});

	const units = [
		{ label: 'minutes', secs: 60 },
		{ label: 'hours', secs: 3600 }
	];

	// The form adds an announcement, or edits the one in `editing`.
	let editing = $state.raw<Announcement | null>(null);
	let message = $state('');
	let every = $state(30);
	let unit = $state(60);
	let everywhere = $state(false);
	let saving = $state(false);

	const encoder = new TextEncoder();
	const size = $derived(encoder.encode(message.trim()).length);
	const interval = $derived(Math.round(every * unit));
	const valid = $derived(size > 0 && size <= 200 && interval >= 60 && interval <= 7 * 86400);

	function reset() {
		editing = null;
		message = '';
		every = 30;
		unit = 60;
		everywhere = false;
	}
	function edit(a: Announcement) {
		editing = a;
		message = a.message;
		unit = a.interval % 3600 === 0 ? 3600 : 60;
		every = a.interval / unit;
		everywhere = a.instance === '*';
	}

	const body = (a: { message: string; interval: number; enabled: boolean; instance: string }) => ({
		message: a.message,
		interval: a.interval,
		enabled: a.enabled,
		allServers: a.instance === '*'
	});

	async function save(e: SubmitEvent) {
		e.preventDefault();
		saving = true;
		const next = { message: message.trim(), interval, enabled: editing?.enabled ?? true, instance: everywhere ? '*' : ctx.id };
		const ok = editing
			? await attempt(() => api.patch(`/instances/${ctx.id}/announcements/${editing!.id}`, body(next)), 'Saved')
			: await attempt(() => api.post(`/instances/${ctx.id}/announcements`, body(next)), 'Added');
		saving = false;
		if (ok) {
			reset();
			load();
		}
	}
	const busy = new Busy();
	async function toggle(a: Announcement, enabled: boolean) {
		const r = await busy.run(`toggle:${a.id}`, () => attempt(() => api.patch(`/instances/${ctx.id}/announcements/${a.id}`, body({ ...a, enabled }))));
		if (r) load();
		return r;
	}
	async function remove(a: Announcement) {
		const shared = a.instance === '*' ? ' It goes from every server.' : '';
		if (!(await ask({ title: 'Delete this announcement?', body: `“${a.message}”${shared}`, action: 'Delete' }))) return;
		if (await busy.run(`remove:${a.id}`, () => attempt(() => api.del(`/instances/${ctx.id}/announcements/${a.id}`), 'Removed'))) {
			if (editing?.id === a.id) reset();
			load();
		}
	}
	function sendNow(a: Announcement) {
		return busy.run(`send:${a.id}`, () => attempt(() => api.post(`/instances/${ctx.id}/say`, { message: a.message }), 'Sent'));
	}

	function cadence(secs: number) {
		const h = Math.floor(secs / 3600);
		const m = Math.round((secs % 3600) / 60);
		if (h && m) return `every ${h} h ${m} min`;
		if (h) return h === 1 ? 'every hour' : `every ${h} hours`;
		return m === 1 ? 'every minute' : `every ${m} minutes`;
	}
	const mine = (a: Announcement) => a.instance !== '*' || allServers;
	const running = $derived(ctx.live.state?.state === 'running');

	guardUnsaved(() =>
		editing ? message !== editing.message || interval !== editing.interval || everywhere !== (editing.instance === '*') : message.trim() !== ''
	);
</script>

<div class="mx-auto max-w-4xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	{#if error}<LoadError {error} onretry={load} />{/if}

	<section class="card">
		<div class="border-b-2 border-line px-4 py-3">
			<h2 class="flex items-center gap-2">
				{editing ? 'Edit announcement' : 'New announcement'}
				<InfoTip
					label="About announcements"
					text={'Sent to chat as "Server" while players are on. When the server empties, the countdowns start over, so nobody who joins gets every message at once. Announcements on one server go out at least a minute apart.'}
				/>
			</h2>
			<p class="mt-0.5 text-xs text-faint">Point players at your Discord, rules or other servers.</p>
		</div>
		<form onsubmit={save} class="space-y-3 px-4 py-4">
			<div>
				<input class="input" placeholder="Join our Discord: discord.gg/…" aria-label="Message" bind:value={message} required />
				<div class="mt-1 text-right text-xs tabular-nums {size > 200 ? 'text-bad' : 'text-faint'}">{size}/200</div>
			</div>
			<div class="flex flex-wrap items-center gap-2 text-sm">
				<span class="text-muted">Every</span>
				<input class="input w-20 tabular-nums" type="number" min="1" step="1" aria-label="How often" bind:value={every} required />
				<select class="input w-32" aria-label="Unit" bind:value={unit}>
					{#each units as u (u.secs)}<option value={u.secs}>{u.label}</option>{/each}
				</select>
				<span class="ml-2 text-muted">on</span>
				<select class="input w-44" aria-label="Where" bind:value={everywhere} disabled={!allServers}>
					<option value={false}>this server</option>
					<option value={true}>every server</option>
				</select>
				{#if !allServers}<InfoTip label="Why not every server" text="Announcements for every server need this permission on all servers." />{/if}
				<div class="ml-auto flex gap-2">
					{#if editing}<button type="button" class="btn" onclick={reset}>Cancel</button>{/if}
					<button class="btn btn-primary" disabled={!valid || saving}>
						<Icon name={editing ? 'check' : 'plus'} size={14} />
						{editing ? 'Save' : 'Add'}
					</button>
				</div>
			</div>
			{#if interval < 60 || interval > 7 * 86400}<p class="text-xs text-bad">Pick 1 minute to 7 days.</p>{/if}
		</form>
	</section>

	<section class="card">
		<div class="border-b-2 border-line px-4 py-3">
			<h2 class="flex items-center gap-2">Announcements {#if loaded}<span class="sticker sticker-accent">{list.length}</span>{/if}</h2>
		</div>
		{#if list.length}
			<ul class="divide-y-2 divide-line">
				{#each list as a (a.id)}
					<li class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3 {a.enabled ? '' : 'opacity-60'} {editing?.id === a.id ? 'bg-sunken/40' : ''}">
						<div class="w-full min-w-0 sm:w-auto sm:flex-1">
							<div class="break-words">{a.message}</div>
							<div class="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted">
								<span>{cadence(a.interval)}</span>
								{#if a.instance === '*'}<span class="sticker sticker-info">every server</span>{/if}
								{#if !a.enabled}<span class="text-faint">paused</span>{/if}
							</div>
						</div>
						<div class="ml-auto flex items-center gap-2">
							{#if ctx.can('players.chat')}
								<button class="btn btn-sm" disabled={!running || busy.is()} title={running ? 'Send it to chat now' : 'The server is not running'} onclick={() => sendNow(a)}>
									<Icon name="send" size={12} /> Send now
								</button>
							{/if}
							{#if mine(a)}
								<label class="flex cursor-pointer items-center" title={a.enabled ? 'Pause' : 'Resume'}>
									<input type="checkbox" aria-label="On its timer" class="peer sr-only" checked={a.enabled} disabled={busy.is()} onchange={(e) => flip(e, (on) => toggle(a, on))} />
									<span class="switch"></span>
								</label>
								<button class="btn btn-sm" onclick={() => edit(a)}>Edit</button>
								<button class="btn btn-sm btn-danger" aria-label="Delete this announcement" disabled={busy.is()} onclick={() => remove(a)}><Icon name="trash" size={12} /></button>
							{/if}
						</div>
					</li>
				{/each}
			</ul>
		{:else if loaded}
			<p class="px-4 py-6 text-sm text-faint">No announcements yet.</p>
		{:else if !error}
			<Loading class="px-4 py-6" />
		{/if}
	</section>
</div>

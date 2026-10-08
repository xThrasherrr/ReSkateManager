<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Perm } from '#lib/api.js';
	import { Live } from '#lib/live.svelte.js';
	import { setLive } from '#lib/context.js';
	import { session } from '#lib/session.svelte.js';
	import { instances } from '#lib/instances.svelte.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { copy } from '#lib/clipboard.js';
	import { duration, stateColor } from '#lib/format.js';
	import Icon, { type IconName } from '#lib/components/Icon.svelte';
	import StateBadge from '#lib/components/StateBadge.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import LoadError from '#lib/components/LoadError.svelte';
	import type { LayoutProps } from './$types';
	import { ask } from '#lib/ask.svelte.js';

	let { children }: LayoutProps = $props();

	const id = $derived(page.params.id as string);
	// The polled list says whether the server is there, and stands in for the
	// live feed until its first snapshot.
	const listed = $derived(instances.byId(id));
	const known = $derived(!!listed);
	const gone = $derived(instances.loaded && !known);
	let live = $state<Live | null>(null);

	// One socket per server page, while the server is on the list; a new id
	// closes the old one.
	$effect(() => {
		if (!known) {
			live = null;
			return;
		}
		const l = new Live(id);
		live = l;
		return () => l.close();
	});

	setLive({
		get live() {
			return live!;
		},
		get id() {
			return id;
		},
		can: (perm: Perm) => session.can(perm, id)
	});

	const st = $derived(live?.state ?? listed);
	// Keep the sidebar in step with the live feed between its polls.
	$effect(() => {
		const s = live?.state;
		const players = live?.count ?? 0;
		if (!s) return;
		// untrack: patch reads the list it writes.
		untrack(() => instances.patch({ ...s, players }));
	});
	const running = $derived(st?.state === 'running' || st?.state === 'starting');
	const moving = $derived(st?.state === 'starting' || st?.state === 'stopping' || st?.state === 'updating');

	let busy = $state('');
	async function act(action: 'start' | 'stop' | 'restart' | 'kill') {
		const n = live?.count ?? 0;
		if ((action === 'stop' || action === 'restart') && n > 0 && st) {
			const go = await ask({
				title: `${action === 'stop' ? 'Stop' : 'Restart'} ${st.name}?`,
				body: `${`${n} player${n === 1 ? ' is' : 's are'} on`}. They are dropped${action === 'restart' ? ' and can rejoin once it is back up' : ''}.`,
				action: action === 'stop' ? 'Stop' : 'Restart'
			});
			if (!go) return;
		}
		busy = action;
		await attempt(() => api.post(`/instances/${id}/${action}`));
		busy = '';
		instances.refresh();
	}
	let confirmKill = $state(false);

	let now = $state(Date.now());
	$effect(() => {
		const t = setInterval(() => (now = Date.now()), 1000);
		return () => clearInterval(t);
	});

	interface Tab {
		href: string;
		label: string;
		icon: IconName;
		perm: Perm;
	}
	const allTabs: Tab[] = [
		{ href: '', label: 'Console', icon: 'terminal', perm: 'console.view' },
		{ href: '/logs', label: 'Logs', icon: 'list', perm: 'console.view' },
		{ href: '/players', label: 'Players', icon: 'users', perm: 'players.view' },
		{ href: '/performance', label: 'Performance', icon: 'activity', perm: 'console.view' },
		{ href: '/bans', label: 'Bans & admins', icon: 'shield', perm: 'players.view' },
		{ href: '/announcements', label: 'Announcements', icon: 'megaphone', perm: 'announcements.manage' },
		{ href: '/settings', label: 'Settings', icon: 'sliders', perm: 'settings.view' },
		{ href: '/world', label: 'World', icon: 'sun', perm: 'settings.view' },
		{ href: '/mods', label: 'Mods', icon: 'package', perm: 'settings.view' },
		{ href: '/updates', label: 'Updates', icon: 'download', perm: 'server.update' },
		{ href: '/options', label: 'Server', icon: 'settings', perm: 'instances.manage' }
	];
	const tabs = $derived(allTabs.filter((t) => session.can(t.perm, id)));
	const base = $derived(`/s/${id}`);

	// The console is the server's first page; without access to it, open the
	// first page they do have.
	$effect(() => {
		if (known && page.url.pathname === base && !session.can('console.view', id) && tabs[0]) goto(base + tabs[0].href, { replace: true });
	});

	// Keep the open tab in view when the strip scrolls sideways on a phone.
	let strip: HTMLElement | undefined = $state();
	$effect(() => {
		page.url.pathname;
		const el = strip?.querySelector<HTMLElement>('[aria-current="page"]');
		if (strip && el) strip.scrollTo({ left: el.offsetLeft - (strip.clientWidth - el.offsetWidth) / 2 });
	});
</script>

{#if gone}
	<div class="mx-auto max-w-md p-6 text-center">
		<h1 class="text-2xl">No such server</h1>
		<p class="mt-2 text-sm text-muted">It may have been removed, or you don't have access to it.</p>
		<a href="/" class="btn mt-4">Back to the dashboard</a>
	</div>
{:else if st && live}
	<div class="flex h-full flex-col">
		<header class="border-b-2 border-ink bg-panel">
			<div class="h-1.5 {stateColor[st.state]} {moving ? 'tape-stripes animate-tape' : ''}"></div>
			<div class="flex flex-wrap items-start gap-x-4 gap-y-3 px-4 pt-3 sm:px-6 sm:pt-4">
				<div class="min-w-0 flex-1">
					<div class="flex flex-wrap items-center gap-x-3 gap-y-1">
						<h1 class="max-w-full truncate font-display text-2xl leading-tight sm:text-3xl">{st.name}</h1>
						<StateBadge state={st.state} />
						{#if !live.connected}<span class="font-marker text-xs text-warn">{live.state ? 'reconnecting…' : 'connecting…'}</span>{/if}
					</div>
					<div class="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted">
						{#if st.state === 'running'}
							<span class="rounded-md bg-raised px-2 py-1"><span class="text-faint">Players</span> <span class="font-semibold text-fg tabular-nums">{live.state ? live.count : st.players}/{st.info.maxPlayers ?? '?'}</span></span>
							<span class="rounded-md bg-raised px-2 py-1"><span class="text-faint">Map</span> <span class="font-semibold text-fg">{st.info.map}</span></span>
							<span class="rounded-md bg-raised px-2 py-1"><span class="text-faint">Up</span> <span class="font-semibold text-fg tabular-nums">{duration(st.readyAt, now)}</span></span>
							{#if listed?.serverVersion}
								<span class="rounded-md bg-raised px-2 py-1"><span class="text-faint">ReSkate</span> <span class="font-semibold text-fg tabular-nums">{listed.serverVersion}</span></span>
							{/if}
							{#if st.info.joinCode}
								<button class="inline-flex items-center gap-1.5 rounded-md border-2 border-dashed border-accent/60 px-2 py-0.5 transition-colors hover:border-accent hover:bg-accent/10 hover:text-fg" onclick={() => copy(st.info.joinCode!)} title="Copy join code">
									<span class="text-faint">Join code</span> <span class="font-mono font-semibold text-accent">{st.info.joinCode}</span> <Icon name="copy" size={12} />
								</button>
							{/if}
						{:else if st.state === 'crashed' && st.lastError}
							<span class="text-bad">{st.lastError}</span>
						{:else if !st.installed}
							<span class="text-warn">Server files are not installed yet.</span>
						{:else}
							<span class="font-mono break-all text-faint">{st.dir}</span>
						{/if}
					</div>
				</div>
				{#if session.can('server.lifecycle', id)}
					<div class="flex items-center gap-2">
						{#if running}
							<button class="btn" disabled={!!busy || st.state === 'starting'} onclick={() => act('restart')}><Icon name="restart" size={14} /> Restart</button>
							<button class="btn btn-danger" disabled={!!busy} onclick={() => act('stop')}><Icon name="stop" size={14} /> {busy === 'stop' ? 'Stopping…' : 'Stop'}</button>
						{:else if st.state === 'stopping'}
							<button class="btn btn-danger" disabled={!!busy} onclick={() => (confirmKill = true)}><Icon name="power" size={14} /> Kill</button>
						{:else}
							<button class="btn btn-primary" disabled={!!busy || !st.installed || st.state === 'updating'} onclick={() => act('start')}
								><Icon name="play" size={14} /> Start</button
							>
						{/if}
					</div>
				{/if}
			</div>
			<nav bind:this={strip} class="no-scrollbar mt-3 flex gap-1 overflow-x-auto px-4 pb-3 text-sm sm:mt-4 sm:gap-1.5 sm:px-6 md:flex-wrap md:overflow-visible">
				{#each tabs as t (t.href)}
					{@const active = page.url.pathname === base + t.href}
					<a
						href={base + t.href}
						aria-current={active ? 'page' : undefined}
						class="flex shrink-0 items-center gap-1.5 rounded-lg border-2 px-3 py-1.5 font-semibold whitespace-nowrap transition-colors {active
							? 'border-ink bg-fg text-ink shadow-ledge-sm'
							: 'border-transparent text-muted hover:bg-raised hover:text-fg'}"
					>
						<Icon name={t.icon} size={14} />
						{t.label}
					</a>
				{/each}
			</nav>
		</header>
		<div class="min-h-0 flex-1">
			<!-- Remounted per server, so no page shows, or acts on, the last one's data. -->
			{#key id}
				{@render children()}
			{/key}
		</div>
	</div>
{:else}
	<div class="grid h-full place-items-center p-6">
		{#if instances.error && !instances.loaded}
			<LoadError error={instances.error} onretry={() => instances.refresh()} />
		{:else}
			<Loading text="connecting…" />
		{/if}
	</div>
{/if}

<Modal bind:open={confirmKill} title="Kill the server process?">
	<p class="text-sm text-muted">The server is not answering the stop request. Killing it skips the Steam logoff and players are dropped without a message.</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (confirmKill = false)}>Cancel</button>
		<button
			class="btn btn-danger"
			onclick={() => {
				confirmKill = false;
				act('kill');
			}}>Kill</button
		>
	{/snippet}
</Modal>

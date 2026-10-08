<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import { MediaQuery } from 'svelte/reactivity';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/state';
	import { session } from '#lib/session.svelte.js';
	import { instances } from '#lib/instances.svelte.js';
	import Icon from '#lib/components/Icon.svelte';
	import Toasts from '#lib/components/Toasts.svelte';
	import Uploads from '#lib/components/Uploads.svelte';
	import Modal from '#lib/components/Modal.svelte';
	import Asker from '#lib/components/Asker.svelte';
	import { afterSignIn, api, loginPath, message, sleep } from '#lib/api.js';
	import { attempt, onRaise, toast } from '#lib/toast.svelte.js';
	import { httpUrl, stateColor, stateLabel } from '#lib/format.js';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	const publicRoutes = ['/login', '/setup'];
	const isPublic = $derived(publicRoutes.includes(page.url.pathname));

	// Who is signed in decides where the panel opens. Until the manager
	// answers, say why the page is still loading, and keep asking.
	let bootError = $state('');
	onMount(() => {
		(async () => {
			for (let tries = 0; ; tries++) {
				try {
					await session.refresh();
					bootError = '';
					break;
				} catch (e) {
					bootError = message(e);
					await sleep(Math.min(2000 * 2 ** tries, 15000));
				}
			}
			const here = location.pathname;
			if (session.setupRequired) {
				if (here !== '/setup') goto('/setup', { replace: true });
			} else if (here === '/setup') goto(session.user ? '/' : '/login', { replace: true });
			else if (!session.user && here !== '/login') goto(loginPath(here + location.search), { replace: true });
			else if (session.user && here === '/login') goto(afterSignIn(page.url.searchParams.get('next')), { replace: true });
		})();
	});

	// Poll the server list while signed in. A boolean, so a session refresh that
	// keeps the same user does not restart the poll.
	const signedIn = $derived(!!session.user);
	$effect(() => {
		if (!signedIn) return;
		instances.start();
		return () => instances.stop();
	});

	const path = $derived(page.url.pathname);

	// "Official EU" -> "OE", "Lobby" -> "LO": two letters tell servers apart on the rail.
	function initials(name: string) {
		const [first = '', second] = name.trim().split(/[\s\-_]+/).filter(Boolean);
		return (second ? first.charAt(0) + second.charAt(0) : first.slice(0, 2) || '?').toUpperCase();
	}

	// Phones get the sidebar as a drawer; wide screens can fold it to an icon rail.
	const desktop = new MediaQuery('min-width: 64rem');
	// The menu buttons open and close it; it closes again whenever the screen
	// crosses the desktop width.
	let drawer = $derived.by(() => {
		desktop.current;
		return false;
	});
	let rail = $state(false);
	const folded = $derived(rail && desktop.current);
	let navigated = false;
	afterNavigate(() => {
		drawer = false;
		navigated = true;
	});

	// The open drawer holds the focus; closed, it hands it back to the menu
	// button, unless a link in it was followed and the new page has it.
	let menuButton: HTMLButtonElement | undefined = $state();
	let closeButton: HTMLButtonElement | undefined = $state();
	let menu: HTMLElement | undefined = $state();
	const drawerOpen = $derived(drawer && !desktop.current);
	let wasOpen = false;
	$effect(() => {
		const open = drawerOpen;
		if (open) closeButton?.focus();
		else if (wasOpen && !navigated && (menu?.contains(document.activeElement) || document.activeElement === document.body)) menuButton?.focus();
		wasOpen = open;
		navigated = false;
	});

	// A page that crashes shows why, in place, rather than leaving a blank
	// area; going to another page tries again.
	let retryPage: (() => void) | null = null;
	afterNavigate(() => {
		retryPage?.();
		retryPage = null;
	});
	onMount(() => {
		try {
			rail = localStorage.getItem('rsm.rail') === '1';
		} catch {
			/* storage may be unavailable */
		}
	});
	function toggleRail() {
		rail = !rail;
		try {
			localStorage.setItem('rsm.rail', rail ? '1' : '0');
		} catch {
			/* storage may be unavailable */
		}
	}

	let confirmUpdate = $state(false);
	let installing = $state(false);
	let restarting = $state('');

	async function installUpdate() {
		confirmUpdate = false;
		installing = true;
		const r = await attempt(() => api.post<{ version: string }>('/manager/update'));
		installing = false;
		if (!r) return;
		restarting = r.version;
		// The manager is down for a few seconds; reload once the new one answers.
		const until = Date.now() + 120_000;
		while (Date.now() < until) {
			await new Promise((ok) => setTimeout(ok, 2000));
			try {
				if ((await api.get<{ version: string }>('/auth/me')).version === r.version) return location.reload();
			} catch {
				// still restarting
			}
		}
		restarting = '';
		toast.error('The manager has not come back. Check the machine it runs on.');
	}

	// Shown for good; raised again (shown anew) over a dialog that opens.
	let stack: HTMLElement | undefined = $state();
	$effect(() => {
		const el = stack;
		if (!el) return;
		const raise = () => {
			if (el.matches(':popover-open')) el.hidePopover();
			el.showPopover();
		};
		raise();
		onRaise(raise);
		return () => onRaise(null);
	});
</script>

<Asker />

<svelte:window onkeydown={(e) => e.key === 'Escape' && (drawer = false)} />

<!-- A popover, so it can sit above an open dialog (the top layer); at the
     top on phones, clear of the bars along the bottom. -->
<div
	bind:this={stack}
	popover="manual"
	class="pointer-events-none fixed inset-x-4 top-4 bottom-auto m-0 flex flex-col gap-2 overflow-visible border-0 bg-transparent p-0 sm:inset-x-auto sm:top-auto sm:right-4 sm:bottom-4 sm:w-96"
>
	<Toasts />
	{#if session.user}<Uploads />{/if}
</div>

{#if isPublic}
	{@render children()}
{:else if session.loaded && session.user}
	{@const up = session.meta?.managerUpdate}
	<div class="flex h-dvh overflow-hidden">
		{#if drawer}
			<button class="fixed inset-0 z-40 bg-black/70 backdrop-blur-[2px] lg:hidden" tabindex="-1" aria-label="Close menu" onclick={() => (drawer = false)}></button>
		{/if}
		<aside
			bind:this={menu}
			id="menu"
			aria-label="Menu"
			data-rail={folded || undefined}
			inert={!desktop.current && !drawer}
			class="fixed inset-y-0 left-0 z-50 flex w-72 max-w-[85vw] flex-col border-r-2 border-ink bg-panel transition-[translate,width] duration-200 ease-out lg:static lg:max-w-none lg:translate-x-0 {drawer
				? 'translate-x-0 shadow-sticker-lg'
				: '-translate-x-full'} {folded ? 'lg:w-[4.5rem]' : 'lg:w-60'}"
		>
			<div class="flex h-16 shrink-0 items-center gap-2 border-b-2 border-line pr-3 rail:justify-center rail:p-0">
				<a href="/" class="group flex min-w-0 flex-1 items-center gap-3 self-stretch pl-4 rail:flex-none rail:p-0" aria-label="ReSkateManager">
					<img src="/favicon.png" alt="" class="size-9 shrink-0 -rotate-6 rounded-xl border-2 border-ink shadow-ledge-sm transition-transform group-hover:rotate-3" />
					<div class="leading-none rail:hidden">
						<div class="font-display text-[22px]">reskate<span class="text-pink">.</span></div>
						<div class="mt-0.5 flex items-baseline gap-1.5">
							<span class="-rotate-2 font-marker text-xs text-warn">manager</span>
							<span class="text-[10px] text-faint">{session.version}</span>
						</div>
					</div>
				</a>
				<button bind:this={closeButton} class="btn btn-ghost size-9 px-0 lg:hidden" onclick={() => (drawer = false)} aria-label="Close menu"><Icon name="x" size={18} /></button>
				<button class="btn btn-ghost btn-sm hidden size-8 px-0 lg:inline-flex rail:hidden" onclick={toggleRail} title="Collapse sidebar" aria-label="Collapse sidebar"
					><Icon name="sidebar" /></button
				>
			</div>
			{#if up}
				<div class="mx-3 mt-3 -rotate-1 rounded-lg border-2 border-ink bg-warn px-2.5 py-2 text-xs font-medium text-ink shadow-ledge-sm rail:hidden">
					{#if restarting}
						Restarting into manager {restarting}…
					{:else}
						Manager {up.version} is out.
						<div class="mt-1 flex gap-3">
							<button class="font-bold underline-offset-2 hover:underline" disabled={installing} onclick={() => (confirmUpdate = true)}
								>{installing ? 'Installing…' : 'Install'}</button
							>
							<a href={httpUrl(up.url)} target="_blank" rel="noreferrer" class="underline-offset-2 hover:underline">What's new</a>
						</div>
					{/if}
				</div>
				<button
					class="btn mx-auto mt-3 hidden size-9 bg-warn px-0 text-ink hover:bg-warn/85 rail:inline-flex"
					disabled={installing || !!restarting}
					onclick={() => (confirmUpdate = true)}
					title={restarting ? `Restarting into manager ${restarting}…` : `Manager ${up.version} is out`}
					aria-label="Install manager {up.version}"><Icon name="download" size={16} /></button
				>
			{/if}
			<nav class="flex flex-1 flex-col gap-0.5 overflow-y-auto p-3 text-sm">
				<a href="/" class="nav-link {path === '/' ? 'nav-link-active' : ''}" title={folded ? 'Dashboard' : undefined}>
					<Icon name="home" /> <span class="rail:hidden">Dashboard</span>
				</a>
				{#if session.can('host.view')}
					<a href="/host" class="nav-link {path === '/host' ? 'nav-link-active' : ''}" title={folded ? 'Host' : undefined}>
						<Icon name="cpu" /> <span class="rail:hidden">Host</span>
					</a>
				{/if}
				<div class="nav-heading">Servers</div>
				{#each instances.list as inst (inst.id)}
					{@const active = path.startsWith(`/s/${inst.id}`)}
					<a href="/s/{inst.id}" class="nav-link {active ? 'nav-link-active' : ''}" title={folded ? `${inst.name}: ${stateLabel[inst.state]}` : stateLabel[inst.state]}>
						<span class="size-2.5 shrink-0 rounded-full border-2 border-ink rail:hidden {stateColor[inst.state]}"></span>
						<span class="sr-only">{stateLabel[inst.state]}:</span>
						<!-- On the rail a server is its initials, with the state dot on the corner. -->
						<span class="relative hidden h-7 w-9 place-items-center rounded-md font-display text-sm tracking-wide rail:grid {active ? 'bg-ink/10' : 'bg-raised text-fg'}">
							{initials(inst.name)}
							<span class="absolute -top-1 -right-1 size-2.5 rounded-full border-2 border-ink {stateColor[inst.state]}"></span>
						</span>
						<span class="flex-1 truncate rail:hidden">{inst.name}</span>
						{#if inst.state === 'running'}<span class="text-xs tabular-nums rail:hidden {active ? 'text-ink/70' : 'text-faint'}">{inst.players}/{inst.info.maxPlayers ?? '?'}</span>{/if}
					</a>
				{:else}
					{#if instances.loaded && session.can('instances.manage')}
						<a href="/?new" class="nav-link text-faint" title={folded ? 'Add a server' : undefined}>
							<Icon name="plus" /> <span class="rail:hidden">Add a server</span>
						</a>
					{:else}
						<div class="px-2.5 py-1.5 font-marker text-xs text-faint rail:hidden">
							{instances.loaded ? 'no servers yet' : instances.error ? "can't load servers" : 'loading…'}
						</div>
					{/if}
				{/each}
				{#if session.canAny('settings.view')}
					<a href="/shared-mods" class="nav-link {path === '/shared-mods' ? 'nav-link-active' : ''}" title={folded ? 'Shared mods' : undefined}>
						<Icon name="package" /> <span class="rail:hidden">Shared mods</span>
					</a>
					<a href="/browse" class="nav-link {path === '/browse' ? 'nav-link-active' : ''}" title={folded ? 'Browse mods' : undefined}>
						<Icon name="search" /> <span class="rail:hidden">Browse mods</span>
					</a>
				{/if}
				{#if session.can('panel.users.manage') || session.can('audit.view')}
					<div class="nav-heading">Panel</div>
				{/if}
				{#if session.can('panel.users.manage')}
					<a href="/users" class="nav-link {path === '/users' ? 'nav-link-active' : ''}" title={folded ? 'Users & roles' : undefined}>
						<Icon name="users" /> <span class="rail:hidden">Users &amp; roles</span>
					</a>
				{/if}
				{#if session.can('audit.view')}
					<a href="/audit" class="nav-link {path === '/audit' ? 'nav-link-active' : ''}" title={folded ? 'Audit log' : undefined}>
						<Icon name="list" /> <span class="rail:hidden">Audit log</span>
					</a>
				{/if}
				{#if session.user.owner}
					<a href="/manager" class="nav-link {path === '/manager' ? 'nav-link-active' : ''}" title={folded ? 'Manager' : undefined}>
						<Icon name="settings" /> <span class="rail:hidden">Manager</span>
					</a>
				{/if}
			</nav>
			<div class="flex items-center gap-1 border-t-2 border-line p-3 rail:flex-col">
				<a
					href="/account"
					class="nav-link min-w-0 flex-1 text-sm rail:w-full rail:flex-none {path === '/account' ? 'nav-link-active' : ''}"
					title={folded ? session.user.username : undefined}
				>
					<Icon name="user" />
					<span class="truncate rail:hidden">{session.user.username}</span>
					{#if session.user.owner}<span class="sticker sticker-warn ml-auto rail:hidden">Owner</span>{/if}
				</a>
				<button class="btn btn-ghost btn-sm size-8 px-0" onclick={() => session.logout()} title="Sign out" aria-label="Sign out"><Icon name="logout" /></button>
				<button class="btn btn-ghost btn-sm hidden size-8 px-0 rail:inline-flex" onclick={toggleRail} title="Expand sidebar" aria-label="Expand sidebar"
					><Icon name="sidebar" /></button
				>
			</div>
		</aside>
		<div class="flex min-w-0 flex-1 flex-col" inert={drawerOpen}>
			<header class="flex h-14 shrink-0 items-center gap-2 border-b-2 border-ink bg-panel px-2 lg:hidden">
				<button
					bind:this={menuButton}
					class="btn btn-ghost relative size-10 px-0"
					onclick={() => (drawer = true)}
					aria-label="Open menu"
					aria-controls="menu"
					aria-expanded={drawer}
				>
					<Icon name="menu" size={20} />
					{#if up}<span class="absolute top-1.5 right-1.5 size-2.5 rounded-full border-2 border-ink bg-warn"></span>{/if}
				</button>
				<a href="/" class="flex items-center gap-2" aria-label="ReSkateManager">
					<img src="/favicon.png" alt="" class="size-7 -rotate-6 rounded-lg border-2 border-ink" />
					<span class="font-display text-xl leading-none">reskate<span class="text-pink">.</span></span>
				</a>
			</header>
			<main class="min-h-0 flex-1 overflow-y-auto">
				<svelte:boundary
					onerror={(e, reset) => {
						console.error(e);
						retryPage = reset;
					}}
				>
					{@render children()}
					{#snippet failed(error, reset)}
						<div class="mx-auto max-w-md p-6 text-center">
							<h1 class="page-title">This page broke</h1>
							<p class="mt-2 text-sm text-muted">Something in it went wrong while it was showing. Try again, or reload the page.</p>
							<details class="mt-2 text-xs text-faint">
								<summary class="cursor-pointer">Details</summary>
								<p class="mt-1 font-mono break-words">{message(error)}</p>
							</details>
							<button class="btn mt-4" onclick={reset}>Try again</button>
						</div>
					{/snippet}
				</svelte:boundary>
			</main>
		</div>
	</div>
	{#if up}
		<Modal bind:open={confirmUpdate} title="Update the manager to {up.version}?">
			<p class="text-sm text-muted">
				The manager downloads {up.version}, checks it, and restarts into it. Every server stops for the restart, so players are dropped;
				the servers running now start again afterwards. This page reconnects by itself.
			</p>
			{#snippet actions()}
				<button class="btn" onclick={() => (confirmUpdate = false)}>Cancel</button>
				<button class="btn btn-primary" onclick={installUpdate}>Update and restart</button>
			{/snippet}
		</Modal>
	{/if}
{:else}
	<div class="grid h-dvh place-items-center p-6 text-center">
		<div>
			<div class="font-marker text-faint">loading…</div>
			{#if bootError}<p class="mt-2 max-w-sm text-sm text-warn" role="status">{bootError} Trying again…</p>{/if}
		</div>
	</div>
{/if}

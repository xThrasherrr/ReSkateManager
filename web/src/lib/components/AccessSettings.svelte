<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message } from '#lib/api.js';
	import { attempt } from '#lib/toast.svelte.js';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	type Proxy = '' | 'cloudflare' | 'forwarded';
	interface Access {
		proxy: Proxy;
		publicUrl: string;
		locked: string[];
		suggested: Proxy;
	}

	let {
		suggest = false,
		saveLabel = 'Save',
		onsaved,
		onskip
	}: {
		/** Pre-select the proxy this page seems to come through (first setup). */
		suggest?: boolean;
		saveLabel?: string;
		onsaved?: () => void;
		/** Shows a "Decide later" button. */
		onskip?: () => void;
	} = $props();

	let loaded = $state.raw<Access | null>(null);
	let shown = $state.raw({ choice: '', address: '' }); // what the form started from
	let choice = $state<Proxy>('');
	let address = $state('');
	let busy = $state(false);

	const lockedProxy = $derived(!!loaded?.locked.includes('proxy'));
	const lockedAddress = $derived(!!loaded?.locked.includes('publicUrl'));

	const options: { value: Proxy; title: string; hint: string }[] = [
		{ value: '', title: 'With an address like 192.168.1.20:40125', hint: 'Or localhost. You on this computer, or people on your home network.' },
		{ value: 'cloudflare', title: 'With a web address through Cloudflare', hint: 'A Cloudflare Tunnel, or a domain that Cloudflare sits in front of.' },
		{ value: 'forwarded', title: 'With a web address through something else', hint: 'nginx, Caddy, Traefik or a hosting panel puts it on your domain.' }
	];
	const via = $derived(choice === 'cloudflare' ? 'Cloudflare' : 'your web server');

	let loadError = $state('');
	async function load() {
		loadError = '';
		try {
			const s = await api.get<Access>('/manager/settings');
			choice = suggest && !s.proxy && !s.locked.includes('proxy') ? s.suggested : s.proxy;
			// Someone on an https:// address is most likely on the one they want to keep.
			address = s.publicUrl || (location.protocol === 'https:' ? location.origin : '');
			loaded = s;
			shown = { choice, address };
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(load);

	async function save(e: SubmitEvent) {
		e.preventDefault();
		// Docker settings fix both; there is nothing to save.
		if (lockedProxy && lockedAddress) return onsaved?.();
		const body: Record<string, string> = {};
		if (!lockedProxy) body.proxy = choice;
		if (!lockedAddress) body.publicUrl = address;
		busy = true;
		const s = await attempt(() => api.patch<Access>('/manager/settings', body), 'Saved');
		busy = false;
		if (!s) return;
		loaded = s;
		shown = { choice, address };
		onsaved?.();
	}

	guardUnsaved(() => !!loaded && (choice !== shown.choice || address !== shown.address));
</script>

{#if !loaded}
	{#if loadError}
		<div class="space-y-3">
			<LoadError error={loadError} onretry={load} />
			{#if onskip}<button type="button" class="btn btn-ghost h-10" onclick={onskip}>Decide later</button>{/if}
		</div>
	{:else}
		<Loading />
	{/if}
{:else}
	<form class="space-y-4" onsubmit={save}>
		<fieldset class="space-y-2" disabled={lockedProxy}>
			<legend class="label">How do people open this panel?</legend>
			{#if lockedProxy}
				<p class="text-xs text-muted">Set by <code class="font-mono">RSM_PROXY</code> in the manager's Docker settings (<code class="font-mono">compose.yaml</code>).</p>
			{/if}
			{#each options as o (o.value)}
				<label
					class="flex items-start gap-2 rounded-xl border-2 p-3 text-sm transition-colors {lockedProxy ? 'cursor-not-allowed' : 'cursor-pointer'} {choice === o.value
						? 'border-accent bg-accent/10'
						: 'border-line-strong hover:border-muted'}"
				>
					<input type="radio" bind:group={choice} value={o.value} class="mt-0.5" />
					<span>
						<span class="font-medium">{o.title}</span>
						{#if o.value && o.value === loaded.suggested}<span class="sticker sticker-info ml-1.5">Looks like this one</span>{/if}
						<br /><span class="text-xs text-muted">{o.hint}</span>
					</span>
				</label>
			{/each}
		</fieldset>

		<div>
			<label class="label" for="addr">Web address{choice ? '' : ' (optional)'}</label>
			<input id="addr" class="input h-9 font-mono" bind:value={address} disabled={lockedAddress} placeholder="https://panel.example.com" inputmode="url" />
			<p class="mt-1 text-xs text-faint">
				{#if lockedAddress}
					Set by <code class="font-mono">RSM_PUBLIC_URL</code> in the manager's Docker settings.
				{:else if choice}
					What people type to open the panel. Steam sign-in sends them back here.
				{:else}
					What people type to open the panel, like <code class="font-mono">http://192.168.1.20:40125</code>. Steam sign-in needs it,
					except on this computer.
				{/if}
			</p>
		</div>

		{#if choice}
			<p class="rounded-xl border-2 border-warn/60 bg-warn/10 p-3 text-xs text-muted">
				<span class="font-semibold text-warn">Keep port 40125 closed to the internet.</span>
				Don't forward or open it on your router/host: {via} already brings visitors in. If people can reach the port directly, they can hide who they are from the sign-in limits and the audit log.
			</p>
		{/if}

		<div class="flex flex-wrap items-center gap-2">
			<button class="btn btn-primary h-10 px-4 text-base" disabled={busy || (lockedProxy && lockedAddress && !onsaved)}>{busy ? 'Saving…' : lockedProxy && lockedAddress ? 'Continue' : saveLabel}</button>
			{#if onskip}<button type="button" class="btn btn-ghost h-10" onclick={onskip}>Decide later</button>{/if}
		</div>
		{#if onskip}<p class="text-xs text-faint">You can change this any time under Manager in the menu.</p>{/if}
	</form>
{/if}

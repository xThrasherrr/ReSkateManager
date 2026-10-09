<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message, type DiscordSettings } from '#lib/api.js';
	import { attempt } from '#lib/toast.svelte.js';
	import { ask } from '#lib/ask.svelte.js';
	import { ago, httpUrl } from '#lib/format.js';
	import Icon from './Icon.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	let saved = $state.raw<DiscordSettings | null>(null);
	let token = $state(''); // a new token to save; the saved one never comes back
	let channel = $state('');
	let joinCodes = $state(true);
	let title = $state('');
	let busy = $state(false);
	let testing = $state(false);

	function show(s: DiscordSettings) {
		saved = s;
		token = '';
		channel = s.channel;
		joinCodes = s.joinCodes;
		title = s.title;
	}
	let loadError = $state('');
	async function load() {
		loadError = '';
		try {
			show(await api.get<DiscordSettings>('/manager/discord'));
		} catch (e) {
			loadError = message(e);
		}
	}
	// The message is edited every minute or so: keep its time and any problem current.
	async function refresh() {
		if (!saved || busy) return;
		try {
			const s = await api.get<DiscordSettings>('/manager/discord');
			if (saved) saved = { ...saved, status: s.status };
		} catch {
			// the next one may get through
		}
	}
	onMount(() => {
		load();
		const t = setInterval(refresh, 30_000);
		return () => clearInterval(t);
	});

	// A bot's ID is the first part of its token, so the invite works before the token is saved.
	function inviteFor(t: string): string | undefined {
		const first = t.trim().replace(/^Bot\s+/, '').split('.')[0] ?? '';
		try {
			const id = atob(first.padEnd(Math.ceil(first.length / 4) * 4, '='));
			if (/^\d{17,20}$/.test(id)) return `https://discord.com/oauth2/authorize?client_id=${id}&scope=bot&permissions=19456`;
		} catch {
			// not a token
		}
		return undefined;
	}
	const invite = $derived(httpUrl(token.trim() ? inviteFor(token) : saved?.invite));

	const changed = $derived(!!saved && (token.trim() !== '' || channel.trim() !== saved.channel || joinCodes !== saved.joinCodes || title.trim() !== saved.title));
	const on = $derived(!!saved?.tokenSet && !!saved.channel);
	const status = $derived(saved?.status ?? {});

	async function save(e: SubmitEvent) {
		e.preventDefault();
		const body: Record<string, unknown> = { channel: channel.trim(), joinCodes, title: title.trim() };
		if (token.trim()) body.token = token.trim();
		busy = true;
		const s = await attempt(() => api.patch<DiscordSettings>('/manager/discord', body), 'Saved');
		busy = false;
		if (s) show(s);
	}

	// Saving nothing new tries Discord again at once, as after inviting the bot.
	async function retry() {
		busy = true;
		const s = await attempt(() => api.patch<DiscordSettings>('/manager/discord', {}));
		busy = false;
		if (s) saved = { ...saved!, status: s.status };
	}

	async function removeToken() {
		const go = await ask({
			title: 'Remove the bot token?',
			body: 'The status message is deleted from the channel, and nothing more is posted until a token is saved again.',
			action: 'Remove'
		});
		if (!go) return;
		busy = true;
		const s = await attempt(() => api.patch<DiscordSettings>('/manager/discord', { token: '' }), 'Removed the bot token');
		busy = false;
		if (!s) return;
		saved = s;
		token = '';
	}

	// Sends with what's in the boxes, saved or not, so they can be tried first.
	async function test() {
		testing = true;
		await attempt(
			() => api.post<{ channel?: string }>('/manager/discord/test', { token: token.trim(), channel: channel.trim() }),
			(r) => `Sent a test message${r.channel ? ` to #${r.channel}` : ''}; check the channel`
		);
		testing = false;
	}

	guardUnsaved(() => changed);
</script>

{#if !saved}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
{:else}
	<form class="space-y-4" onsubmit={save}>
		<p class="text-sm text-muted">
			A bot posts one message in a Discord channel and keeps it up to date: whether each server is online, its players, map, join code, ReSkate version and next
			restart. It's edited at most once a minute, and says when it was last updated, so an old time means the manager isn't running.
		</p>
		<details class="text-xs text-faint">
			<summary class="cursor-pointer">How to set up the bot</summary>
			<ol class="mt-1.5 list-decimal space-y-1 pl-4">
				<li>
					In the <a class="text-accent hover:underline" href="https://discord.com/developers/applications" target="_blank" rel="noreferrer">Developer Portal</a>, choose
					<span class="text-muted">New Application</span>, then on its <span class="text-muted">Bot</span> page <span class="text-muted">Reset Token</span> and copy the token.
				</li>
				<li>Paste it below, then use <span class="text-muted">Invite the bot</span> to add it to your Discord server. It asks only to see the channel, send messages and embed links.</li>
				<li>
					In Discord, turn on <span class="text-muted">Developer Mode</span> (User Settings, Advanced), right-click the channel and choose
					<span class="text-muted">Copy Channel ID</span>. Paste it below and save.
				</li>
			</ol>
		</details>

		<div>
			<label class="label" for="discord-token">Bot token</label>
			<div class="flex flex-wrap gap-2">
				<input
					id="discord-token"
					class="input min-w-0 flex-1 font-mono text-xs"
					type="password"
					placeholder={saved.tokenSet ? 'Saved; paste a new one to replace it' : 'Paste the bot token'}
					bind:value={token}
					autocomplete="off"
					spellcheck="false"
				/>
				{#if saved.tokenSet}<button type="button" class="btn btn-danger" disabled={busy} onclick={removeToken}><Icon name="trash" size={14} /> Remove</button>{/if}
			</div>
			<p class="mt-1 text-xs text-faint">Kept in the manager and never shown again: a bot token can do far more than post here.</p>
			{#if invite}
				<a class="mt-1.5 inline-flex items-center gap-1 text-xs text-accent hover:underline" href={invite} target="_blank" rel="noreferrer"
					><Icon name="external" size={12} /> Invite the bot to your Discord server</a
				>
			{/if}
		</div>

		<label class="block">
			<span class="label">Channel</span>
			<input class="input font-mono text-xs" placeholder="Channel ID, or a link to the channel" bind:value={channel} autocomplete="off" spellcheck="false" />
		</label>

		<label class="block">
			<span class="label">Title</span>
			<input class="input" placeholder={saved.defaultTitle} maxlength="256" bind:value={title} autocomplete="off" />
			<span class="mt-1 block text-xs text-faint">The heading at the top of the message. Left empty, it's "{saved.defaultTitle}".</span>
		</label>

		<label class="flex items-start gap-2 text-sm">
			<input type="checkbox" class="mt-0.5" bind:checked={joinCodes} />
			<span>
				<span class="text-fg">Show join codes</span>
				<span class="block text-xs text-faint">Players type a running server's code in the game to join it. A server with a password still asks for it.</span>
			</span>
		</label>

		{#if on && status.problem}
			<div class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border-2 px-4 py-3 text-sm {status.stopped ? 'border-bad/50 bg-bad/10' : 'border-warn/50 bg-warn/10'}" role="alert">
				<Icon name="alert" size={14} class={status.stopped ? 'text-bad' : 'text-warn'} />
				<p class="min-w-0 flex-1 text-fg">{status.problem}</p>
				<button type="button" class="btn btn-sm" disabled={busy || changed} onclick={retry}><Icon name="restart" size={12} /> Try again</button>
			</div>
		{/if}
		{#if on && status.updated}
			<p class="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted">
				<span><Icon name="check" size={14} class="inline text-ok" /> Posted{status.channel ? ` in #${status.channel}` : ''}, updated {ago(status.updated)}</span>
				{#if httpUrl(status.link)}
					<a class="inline-flex items-center gap-1 text-xs text-accent hover:underline" href={httpUrl(status.link)} target="_blank" rel="noreferrer"
						><Icon name="external" size={12} /> Open in Discord</a
					>
				{/if}
			</p>
		{:else if !on}
			<p class="text-xs text-faint">Nothing is posted until a bot token and a channel are saved.</p>
		{/if}

		<div class="flex flex-wrap gap-2">
			<button class="btn btn-primary" disabled={!changed || busy}>{busy ? 'Saving…' : 'Save'}</button>
			<button type="button" class="btn" disabled={(!token.trim() && !saved.tokenSet) || !channel.trim() || testing} onclick={test}
				><Icon name="send" size={14} /> Send a test</button
			>
		</div>
	</form>
{/if}

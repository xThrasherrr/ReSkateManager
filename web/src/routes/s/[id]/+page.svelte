<script lang="ts">
	import { api } from '#lib/api.js';
	import { useLive } from '#lib/context.js';
	import { toast } from '#lib/toast.svelte.js';
	import ConsoleView from '#lib/components/ConsoleView.svelte';
	import Icon from '#lib/components/Icon.svelte';
	import Loading from '#lib/components/Loading.svelte';
	import { utf8Length } from '#lib/text.js';

	const ctx = useLive();
	const live = $derived(ctx.live);
	const running = $derived(live.state?.state === 'running');

	let filter = $state('all');
	let search = $state('');
	let line = $state('');
	let sending = $state(false);
	let view: ConsoleView | undefined = $state();

	// Clear hides the lines so far in this browser, and only here: the server
	// keeps them, and the Logs tab has them. Kept across reloads by time, since
	// line numbers start again when the manager restarts.
	const clearKey = $derived(`rsm.cleared.${ctx.id}`);
	let clearedAt = $state(0);
	$effect(() => {
		try {
			clearedAt = Number(localStorage.getItem(clearKey)) || 0;
		} catch {
			clearedAt = 0;
		}
	});
	const entries = $derived(clearedAt ? live.console.filter((e) => e.at > clearedAt) : live.console);
	const hidden = $derived(live.console.length - entries.length);
	function remember(at: number) {
		clearedAt = at;
		try {
			if (at) localStorage.setItem(clearKey, String(at));
			else localStorage.removeItem(clearKey);
		} catch {
			/* storage may be unavailable */
		}
	}
	// By the server's clock, which stamped the lines, not this browser's.
	function clear() {
		remember(live.console.reduce((m, e) => Math.max(m, e.at), 0));
		view?.toBottom();
	}

	// Server commands from the server's help text (Server/server_host.cpp), for Tab completion.
	const commands = [
		'status', 'net', 'players', 'say', 'msg', 'msg-party', 'msg-admins', 'kick', 'ban', 'unban', 'bans', 'map', 'maps', 'name',
		'password', 'welcome', 'listed',
		'voice', 'voice-range', 'distances', 'crowd', 'rate', 'placement', 'objects', 'clear-objects', 'noclip', 'nobail', 'boosts',
		'tuning', 'tpall', 'tphere', 'votes', 'vote-cancel', 'park', 'layer-sync', 'layer', 'layers', 'tod', 'activity-log',
		'announce-throwdowns', 'parties', 'party-size', 'speed-check', 'score-check', 'score-allow', 'reserved', 'admin', 'admins', 'help'
	];

	const historyKey = $derived(`rsm.history.${ctx.id}`);
	let history: string[] = [];
	let cursor = -1;
	$effect(() => {
		try {
			history = JSON.parse(localStorage.getItem(historyKey) ?? '[]');
		} catch {
			history = [];
		}
	});

	async function send(e?: SubmitEvent) {
		e?.preventDefault();
		const text = line.trim();
		if (!text || sending) return;
		sending = true;
		try {
			if (chat) await api.post(`/instances/${ctx.id}/say`, { message: text });
			else await api.post(`/instances/${ctx.id}/command`, { line: text });
			// Not a password: it would sit in this browser's storage for ↑ to bring back.
			if (!chat && !/^password\s/i.test(text)) {
				history = [text, ...history.filter((h) => h !== text)].slice(0, 100);
				try {
					localStorage.setItem(historyKey, JSON.stringify(history));
				} catch {
					/* storage may be unavailable */
				}
			}
			line = '';
			cursor = -1;
			view?.toBottom();
		} catch (err) {
			toast.error(err);
		} finally {
			sending = false;
		}
	}

	function keydown(e: KeyboardEvent) {
		if (e.key === 'ArrowUp' && history.length) {
			e.preventDefault();
			cursor = Math.min(cursor + 1, history.length - 1);
			line = history[cursor] ?? '';
		} else if (e.key === 'ArrowDown') {
			e.preventDefault();
			cursor = Math.max(cursor - 1, -1);
			line = history[cursor] ?? '';
		} else if (e.key === 'Tab' && !chat && line && !line.includes(' ')) {
			const matches = commands.filter((c) => c.startsWith(line.toLowerCase()));
			if (matches.length) {
				e.preventDefault();
				line = matches.length === 1 ? `${matches[0]} ` : commonPrefix(matches);
			}
		}
	}

	function commonPrefix(list: string[]) {
		let p = list[0] ?? '';
		for (const s of list) while (!s.startsWith(p)) p = p.slice(0, -1);
		return p;
	}

	const filters: [string, string][] = [
		['all', 'All'],
		['chat', 'Chat'],
		['players', 'Joins'],
		['admin', 'Admin'],
		['anticheat', 'Anti-cheat'],
		['manager', 'Manager']
	];
	const canExec = $derived(ctx.can('console.exec'));
	const canChat = $derived(ctx.can('players.chat'));
	// Chat mode when chat is all they may send; the mode button overrides it.
	let chat = $derived(!canExec && canChat);

	// Commands and chat are limited in bytes, as the manager counts them.
	const limit = $derived(chat ? 200 : 1024);
	const tooLong = $derived(utf8Length(line.trim()) > limit);
</script>

{#if !live.state}
	<Loading text="connecting…" class="p-6" />
{:else if ctx.can('console.view')}
	<div class="flex h-full flex-col">
		<div class="flex flex-wrap items-center gap-2 border-b-2 border-line bg-panel px-3 py-2 sm:px-4">
			<div class="seg no-scrollbar max-w-full overflow-x-auto" role="group" aria-label="Show">
				{#each filters as [key, label] (key)}
					<button class={filter === key ? 'seg-on' : ''} aria-pressed={filter === key} onclick={() => (filter = key)}>{label}</button>
				{/each}
			</div>
			<div class="flex w-full items-center gap-2 sm:ml-auto sm:w-auto">
				<div class="relative min-w-0 flex-1 sm:w-64 sm:flex-none">
					<Icon name="search" size={14} class="absolute top-1/2 left-2 -translate-y-1/2 text-faint" />
					<input class="input h-8 pl-7 text-xs" placeholder="Search output" aria-label="Search output" bind:value={search} />
				</div>
				<button class="btn btn-sm h-8" onclick={clear} disabled={!entries.length} title="Hide the lines so far, in this browser">
					<Icon name="x" size={13} /> Clear
				</button>
			</div>
		</div>
		<div class="min-h-0 flex-1">
			<ConsoleView bind:this={view} {entries} {filter} {search} empty={hidden ? 'Cleared. New lines show up here.' : 'No output yet.'}>
				{#snippet top()}
					<div class="mb-2 flex flex-wrap gap-x-3 font-sans text-xs text-faint">
						{#if hidden}
							<span>{hidden} earlier line{hidden === 1 ? '' : 's'} cleared.</span>
							<button class="text-accent hover:underline" onclick={() => remember(0)}>Show them</button>
						{:else if live.console.length >= 300}
							<span>Only the newest lines load here.</span>
						{/if}
						<a class="text-accent hover:underline" href="/s/{ctx.id}/logs">Older output is on the Logs tab</a>
					</div>
				{/snippet}
			</ConsoleView>
		</div>
		{#if canExec || canChat}
			<form onsubmit={send} class="flex items-center gap-2 border-t-2 border-line bg-panel px-3 py-2 sm:px-4 sm:py-3">
				{#if canExec && canChat}
					<button
						type="button"
						class="btn h-9 w-9 px-0 sm:w-28 sm:px-3 {chat ? 'bg-info text-ink hover:bg-info' : ''}"
						onclick={() => (chat = !chat)}
						title="Switch between console commands and server chat"
						aria-label="Chat mode"
						aria-pressed={chat}
					>
						<Icon name={chat ? 'chat' : 'terminal'} size={14} />
						<span class="hidden sm:inline">{chat ? 'Chat' : 'Command'}</span>
					</button>
				{/if}
				<div class="relative min-w-0 flex-1">
					<span class="absolute top-1/2 left-2.5 -translate-y-1/2 font-mono text-sm {chat ? 'text-info' : 'text-accent'}">{chat ? 'Server:' : '>'}</span>
					<input
						class="input h-9 font-mono {chat ? 'pl-16' : 'pl-6'}"
						aria-label={chat ? 'Chat message' : 'Console command'}
						placeholder={!running ? (live.state?.state === 'starting' ? 'Waiting for the server to start…' : 'The server is not running') : chat ? 'Message everyone on the server' : 'Type a command — Tab completes, ↑ recalls'}
						disabled={!running}
						bind:value={line}
						onkeydown={keydown}
						maxlength={limit}
						aria-invalid={tooLong}
						autocomplete="off"
						spellcheck="false"
					/>
				</div>
				<button class="btn btn-primary h-9 w-9 px-0 sm:w-auto sm:px-3" disabled={!running || !line.trim() || sending || tooLong} title={tooLong ? `At most ${limit} bytes` : undefined} aria-label="Send"
					><Icon name="send" size={14} /> <span class="hidden sm:inline">Send</span></button
				>
			</form>
		{/if}
	</div>
{:else}
	<div class="p-6 text-sm text-muted">You don't have access to this server's console.</div>
{/if}

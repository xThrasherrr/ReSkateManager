<script module lang="ts">
	import type { PluginLine, PluginState } from '#lib/api.js';

	export { pluginLog };
	export const sticker: Record<PluginState, [string, string]> = {
		off: ['', 'Off'],
		starting: ['sticker-info', 'Starting'],
		running: ['sticker-accent', 'Running'],
		failed: ['sticker-pink', 'Failed'],
		stopped: ['sticker-warn', 'Stopped']
	};
	const levelColor = { info: 'text-muted', warn: 'text-warn', error: 'text-bad' };
	const time = (ms: number) => new Date(ms).toLocaleTimeString();
</script>

<script lang="ts">
	import { onMount } from 'svelte';
	import { api, ApiError, message, sleep, upload, type Plugin, type PluginsInfo } from '#lib/api.js';
	import { ask } from '#lib/ask.svelte.js';
	import { attempt, toast } from '#lib/toast.svelte.js';
	import { bytes, duration, plural } from '#lib/format.js';
	import FolderPath from './FolderPath.svelte';
	import Icon from './Icon.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';

	let info = $state.raw<PluginsInfo | null>(null);
	let loadError = $state('');
	let busy = $state<string | null>(null); // the plugin an action waits on
	let picker = $state<HTMLInputElement>();
	let adding = $state(false);

	async function load() {
		try {
			info = await api.get<PluginsInfo>('/manager/plugins');
			loadError = '';
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(() => {
		load();
		// Running plugins log and count as they go.
		const t = setInterval(() => {
			if (!document.hidden && !busy) load();
		}, 5000);
		return () => clearInterval(t);
	});

	// After it's turned on or reloaded, until it's running or has failed.
	async function settle(id: string) {
		for (let i = 0; i < 40; i++) {
			await load();
			if (info?.plugins.find((p) => p.id === id)?.state !== 'starting') return;
			await sleep(250);
		}
	}

	async function toggle(p: Plugin, on: boolean) {
		busy = p.id;
		const done = await attempt(() => api.patch<Plugin>(`/manager/plugins/${encodeURIComponent(p.id)}`, { enabled: on }), on ? `Turned on ${p.name}` : `Turned off ${p.name}`);
		if (done) await settle(p.id);
		else await load();
		busy = null;
	}

	async function grant(p: Plugin, key: string, on: boolean) {
		busy = p.id;
		const granted = on ? [...p.granted, key] : p.granted.filter((k) => k !== key);
		const label = info?.grants.find((g) => g.key === key)?.label.toLowerCase() ?? key;
		await attempt(() => api.patch<Plugin>(`/manager/plugins/${encodeURIComponent(p.id)}`, { granted }), on ? `${p.name} may now ${label}` : `${p.name} may no longer ${label}`);
		await load();
		busy = null;
	}

	// A zip of a plugin's folder, added off; one there already is replaced when they say so.
	async function add(file: File | undefined) {
		if (!file) return;
		adding = true;
		const send = (replace: boolean) => upload<Plugin>('POST', `/manager/plugins?name=${encodeURIComponent(file.name)}${replace ? '&replace=1' : ''}`, file);
		let replaced = false;
		try {
			const p = await send(false).catch(async (e) => {
				if (!(e instanceof ApiError && e.status === 409)) throw e;
				const old = e.body.plugin as Plugin | undefined;
				replaced = await ask({
					title: `Replace ${old?.name ?? 'it'}?`,
					body: `${e.message}. Its files are replaced with the ones in ${file.name}${old?.enabled ? ', and it starts again with them' : ''}. What it was granted stays; anything new it asks for waits for you to tick it.`,
					action: 'Replace',
					danger: false
				});
				return replaced ? send(true) : null;
			});
			if (p) toast.ok(replaced ? `Replaced ${p.name} with ${p.version || 'the upload'}` : `Added ${p.name}: turn it on below`);
		} catch (e) {
			toast.error(e);
		}
		adding = false;
		await load();
	}

	async function remove(p: Plugin) {
		const body = `Its folder, ${p.id}, is deleted with everything in it${p.enabled ? ', and it stops' : ''}. What it was granted is forgotten.`;
		if (!(await ask({ title: `Remove ${p.name}?`, body, action: 'Remove' }))) return;
		busy = p.id;
		await attempt(() => api.del(`/manager/plugins/${encodeURIComponent(p.id)}`), `Removed ${p.name}`);
		await load();
		busy = null;
	}

	async function reload(p: Plugin) {
		busy = p.id;
		if (await attempt(() => api.post<Plugin>(`/manager/plugins/${encodeURIComponent(p.id)}/reload`), `Reloaded ${p.name}`)) await settle(p.id);
		busy = null;
	}

	const ms = (v: number) => (v < 0.1 ? '<0.1' : v < 10 ? v.toFixed(1) : String(Math.round(v)));
</script>

<!-- A plugin's log, the newest first. -->
{#snippet pluginLog(lines: PluginLine[])}
	<ol class="max-h-64 space-y-0.5 overflow-auto rounded-lg bg-sunken p-2 font-mono text-xs">
		{#each lines.toReversed() as l, i (i)}
			<li class="flex gap-2">
				<span class="shrink-0 text-faint">{time(l.at)}</span>
				<span class="min-w-0 break-words whitespace-pre-wrap {levelColor[l.level]}">{l.text}</span>
			</li>
		{/each}
	</ol>
{/snippet}

{#if !info}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
{:else}
	<div class="@container">
		<div class="grid grid-cols-[minmax(0,1fr)] items-start gap-4 @4xl:grid-cols-[minmax(0,24rem)_minmax(0,1fr)] @4xl:gap-6">
			<div class="space-y-4">
				{#if loadError}<LoadError error={loadError} onretry={load} />{/if}
				<p class="text-sm text-muted">
					Plugins are Lua 5.4 scripts that run inside the manager and act on what happens on your servers: players joining and leaving, chat, throwdowns, map
					and state changes. A plugin runs code on this machine, as a mod does, so only turn on plugins you trust.
				</p>
				<p class="flex items-start gap-2 text-sm">
					<span class="sticker sticker-warn shrink-0">Preview</span>
					<span class="text-muted">Plugin API {info.api} may still change, so a plugin made for it may need changes later.</span>
				</p>
				<div class="flex flex-wrap items-center gap-x-3 gap-y-2">
					<input
						bind:this={picker}
						type="file"
						accept=".zip,application/zip"
						hidden
						onchange={(e) => {
							add(e.currentTarget.files?.[0]);
							e.currentTarget.value = '';
						}}
					/>
					<button class="btn btn-primary" disabled={adding || busy !== null} onclick={() => picker?.click()}>
						<Icon name="upload" size={14} />
						{adding ? 'Adding…' : 'Add plugin'}
					</button>
					<span class="text-xs text-faint">A <code>.zip</code> of its folder, up to {info.maxUploadMB} MB. It starts off.</span>
				</div>
				<FolderPath label="Plugins folder" folder={info.dir} />
				<p class="text-xs text-faint">
					Each plugin is a folder there with a <code>plugin.toml</code> and a <code>main.lua</code>, added above or copied in, and changed with its Edit button. A call into a plugin may run for {info.callMs} ms, and a plugin may use
					{info.memoryMB} MB. One that keeps failing or running over is stopped. Its log is on its card and in <code>data/plugins.log</code>.
				</p>
				<details class="rounded-xl border-2 border-line px-3 py-2 text-sm">
					<summary class="cursor-pointer font-semibold">Make one</summary>
					<div class="mt-2 space-y-2 text-xs">
						<p class="text-muted">A folder <code>welcome</code> with these two files, zipped and added above, or copied into the plugins folder:</p>
						<pre class="overflow-x-auto rounded-lg bg-sunken p-2 font-mono"><span class="text-faint"># plugin.toml</span>
name = "Welcome"
version = "1.0.0"
api = {info.api}
permissions = ["players.chat"]</pre>
						<pre class="overflow-x-auto rounded-lg bg-sunken p-2 font-mono"><span class="text-faint">-- main.lua</span>
on("player.joined", function(e)
  log(e.name .. " joined " .. e.server)
  server.msg(e.server, e.id, "Welcome, " .. e.name)
end)</pre>
						<p class="text-muted">
							Then turn it on below, and tick what it asks to do. <code>on(event, fn)</code> takes {plural(info.events.length, 'event')}:
							{#each info.events as e, i}<code>{e}</code>{i < info.events.length - 1 ? ', ' : ''}{/each}. <code>after(seconds, fn)</code> and
							<code>every(seconds, fn)</code> run it later, <code>cancel(timer)</code> stops one, <code>log(...)</code> writes to its log, and
							<code>require("lib.util")</code> runs <code>lib/util.lua</code> from its folder. <code>server.players(id)</code> and
							<code>server.info(id)</code> read a server; <code>server.say</code>, <code>server.msg</code>, <code>server.kick</code> and the rest act on one, a few a
							second, with what you grant the plugin.
						</p>
					</div>
				</details>
			</div>
			{#if info.plugins.length === 0}
				<p class="rounded-xl border-2 border-dashed border-line px-4 py-6 text-center text-sm text-muted">No plugins in the folder yet.</p>
			{:else}
				<ul class="grid gap-3 @6xl:grid-cols-2">
					{#each info.plugins as p (p.id)}
						{@const [color, label] = sticker[p.state]}
					{@const listens = (p.stats.handlers ?? []).filter((h) => !h.startsWith('command:'))}
						<li class="space-y-2 rounded-xl border-2 border-line p-3">
							<div class="flex flex-wrap items-center gap-x-3 gap-y-2">
								<div class="min-w-0 flex-1">
									<div class="flex flex-wrap items-center gap-2">
										<span class="truncate font-semibold">{p.name}</span>
										{#if p.version}<span class="text-xs text-faint">{p.version}</span>{/if}
										<span class="sticker {color}">{label}</span>
									</div>
									{#if p.name !== p.id}<div class="truncate font-mono text-xs text-faint">{p.id}</div>{/if}
								</div>
								<div class="flex items-center gap-2">
									<a class="btn btn-ghost btn-sm" href="/manager/plugins/{encodeURIComponent(p.id)}" title="Read and change its files">
										<Icon name="pencil" size={14} /> Edit
									</a>
									<button class="btn btn-ghost btn-sm" disabled={busy !== null} title="Read its files again and start it afresh" onclick={() => reload(p)}>
										<Icon name="restart" size={14} /> Reload
									</button>
									<button class="btn btn-ghost btn-sm" disabled={busy !== null || adding} title="Delete its folder" aria-label="Remove {p.name}" onclick={() => remove(p)}>
										<Icon name="trash" size={14} />
									</button>
									<label class="inline-flex cursor-pointer items-center gap-2" title={p.enabled ? 'Turn it off' : 'Turn it on'}>
										<input
											type="checkbox"
											class="peer sr-only"
											checked={p.enabled}
											disabled={busy !== null}
											aria-label="{p.name} on"
											onchange={(e) => toggle(p, e.currentTarget.checked)}
										/>
										<span class="switch"></span>
									</label>
								</div>
							</div>
							{#if p.description}<p class="text-sm text-muted">{p.description}</p>{/if}
							{#if p.problem}
								<p class="flex items-start gap-1.5 text-sm text-bad"><Icon name="alert" size={14} class="mt-0.5 shrink-0" /> {p.problem}</p>
							{:else if p.reason && (p.state === 'failed' || p.state === 'stopped')}
								<p class="flex items-start gap-1.5 text-sm text-warn"><Icon name="alert" size={14} class="mt-0.5 shrink-0" /> <span class="min-w-0 break-words">{p.reason}</span></p>
							{/if}
							{#if p.unknown?.length}
								<p class="text-xs text-faint">plugin.toml has settings that do nothing: {p.unknown.join(', ')}</p>
							{/if}
							<p class="text-xs text-muted">
								{p.servers?.length ? `On ${p.servers.join(', ')}` : 'On every server'}{listens.length ? ` · listens to ${listens.join(', ')}` : ''}
							</p>
							{#if p.commands?.length}
								<p class="text-xs text-muted">
									Answers {p.commands.map((w) => `/${w}`).join(', ')} in chat. A server takes a new command as it next starts.
								</p>
							{/if}
							{#if p.permissions?.length}
								<fieldset class="space-y-1.5">
									<legend class="text-xs font-semibold text-muted">It asks to</legend>
									{#each p.permissions as key (key)}
										{@const g = info.grants.find((g) => g.key === key)}
										{#if g}
											<label class="flex items-start gap-2 text-sm">
												<input type="checkbox" class="mt-0.5" checked={p.granted.includes(key)} disabled={busy !== null} onchange={(e) => grant(p, key, e.currentTarget.checked)} />
												<span class="min-w-0">{g.label}<span class="block text-xs break-words text-faint">{g.calls.join(', ')}</span></span>
											</label>
										{:else}
											<p class="flex items-start gap-2 text-sm text-faint">
												<Icon name="alert" size={14} class="mt-0.5 shrink-0" />
												<span class="min-w-0 break-words"><code>{key}</code>: plugins can't be given this</span>
											</p>
										{/if}
									{/each}
								</fieldset>
							{/if}
							{#if p.state === 'running' || p.stats.calls > 0}
								<p class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-faint tabular-nums">
									{#if p.state === 'running' && p.since}<span>Up {duration(p.since)}</span>{/if}
									<span>{plural(p.stats.events, 'event')}</span>
									{#if p.stats.dropped}<span class="text-warn" title="Events and timers it was too busy for">{p.stats.dropped} dropped</span>{/if}
									{#if p.stats.errors}<span class="text-bad">{plural(p.stats.errors, 'error')}</span>{/if}
									<span title="Its slowest call">slowest {ms(p.stats.slowestMs)} ms</span>
									{#if p.stats.memory}<span>{bytes(p.stats.memory)}</span>{/if}
									{#if p.stats.timers}<span>{plural(p.stats.timers, 'timer')}</span>{/if}
									{#if p.stats.sent}<span title="Commands it gave its servers">{plural(p.stats.sent, 'command')}</span>{/if}
									{#if p.stats.unsent}<span class="text-warn" title="Commands not sent: too many were waiting">{p.stats.unsent} unsent</span>{/if}
									{#if p.restarts}<span class="text-warn" title="Fresh starts after running over its time">{plural(p.restarts, 'restart')}</span>{/if}
								</p>
							{/if}
							{#if p.recent.length}
								<details>
									<summary class="mb-2 cursor-pointer text-xs font-semibold text-muted">Log ({p.recent.length})</summary>
									{@render pluginLog(p.recent)}
								</details>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	</div>
{/if}

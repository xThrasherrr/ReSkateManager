<script lang="ts">
	import { onMount } from 'svelte';
	import { api, message, type AlertSettings } from '#lib/api.js';
	import { attempt } from '#lib/toast.svelte.js';
	import Icon from './Icon.svelte';
	import Loading from './Loading.svelte';
	import LoadError from './LoadError.svelte';
	import { guardUnsaved } from '#lib/unsaved.svelte.js';

	const kinds: Record<string, { label: string; help?: string }> = {
		crash: { label: 'A server crashed', help: 'Each crash it restarts from.' },
		gaveUp: { label: 'A server keeps crashing', help: 'It crashed again after five restarts in a row, and stays down.' },
		stopped: {
			label: 'A server stopped on its own',
			help: "It won't restart: a config problem, a failed restart, or restarting after a crash is off for it."
		},
		updated: { label: 'A server was updated', help: 'By hand or by auto-update.' },
		updateFailed: { label: 'A server update failed' },
		managerUpdated: { label: 'The manager was updated', help: 'It started on a version it had not run before.' },
		backupFailed: { label: 'A scheduled backup failed', help: 'It is tried again an hour later.' },
		diskLow: { label: 'A drive is running out of space', help: "One holding the manager's folders or a server's. Again once it has room." },
		memoryHigh: { label: 'Memory stays high', help: "The machine's, or its container's limit when the manager runs in one. Again once it is back down." }
	};

	let saved = $state.raw<AlertSettings | null>(null);
	let webhook = $state('');
	let off = $state<string[]>([]);
	let diskGB = $state(0);
	let memPct = $state(0);
	let memMinutes = $state(0);
	let busy = $state(false);
	let testing = $state(false);

	function show(s: AlertSettings) {
		saved = s;
		webhook = s.webhook;
		off = [...s.off];
		diskGB = s.diskGB;
		memPct = s.memPct;
		memMinutes = s.memMinutes;
	}
	let loadError = $state('');
	async function load() {
		loadError = '';
		try {
			show(await api.get<AlertSettings>('/manager/alerts'));
		} catch (e) {
			loadError = message(e);
		}
	}
	onMount(load);

	const sorted = (l: string[]) => l.toSorted().join(',');
	const changed = $derived(
		!!saved &&
			(webhook.trim() !== saved.webhook ||
				sorted(off) !== sorted(saved.off) ||
				diskGB !== saved.diskGB ||
				memPct !== saved.memPct ||
				memMinutes !== saved.memMinutes)
	);
	const whole = (n: number, lo: number, hi: number) => Number.isInteger(n) && n >= lo && n <= hi;
	const valid = $derived(whole(diskGB, 1, 10000) && whole(memPct, 50, 99) && whole(memMinutes, 1, 1440));

	async function save(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		const s = await attempt(() => api.patch<AlertSettings>('/manager/alerts', { webhook: webhook.trim(), off, diskGB, memPct, memMinutes }), 'Saved');
		busy = false;
		if (s) show(s);
	}

	// Sends to the address in the box, saved or not, so it can be tried first.
	async function test() {
		testing = true;
		await attempt(() => api.post('/manager/alerts/test', { webhook: webhook.trim() }), 'Sent a test alert; check the channel');
		testing = false;
	}

	guardUnsaved(() => changed);
</script>

{#if !saved}
	{#if loadError}<LoadError error={loadError} onretry={load} />{:else}<Loading />{/if}
{:else}
	<form class="space-y-4" onsubmit={save}>
		<p class="text-sm text-muted">
			Posts to a Discord channel when a server crashes or stops on its own, when updates install or fail, when a backup fails, and when the machine runs low on disk space or
			memory, so you hear of it with the panel closed. In Discord, open the channel's settings, then Integrations, Webhooks, New Webhook, and copy its URL.
		</p>
		<label class="block">
			<span class="label">Webhook URL</span>
			<input
				class="input font-mono text-xs"
				type="url"
				placeholder="https://discord.com/api/webhooks/…"
				bind:value={webhook}
				autocomplete="off"
				spellcheck="false"
			/>
		</label>
		<fieldset class="space-y-2" disabled={!webhook.trim()}>
			<legend class="label">Send an alert when</legend>
			{#each saved.kinds as k (k)}
				<label class="flex items-start gap-2 text-sm">
					<input
						type="checkbox"
						class="mt-0.5"
						checked={!off.includes(k)}
						onchange={(e) => (off = e.currentTarget.checked ? off.filter((x) => x !== k) : [...off, k])}
					/>
					<span>
						<span class="text-fg">{kinds[k]?.label ?? k}</span>
						{#if kinds[k]?.help}<span class="block text-xs text-faint">{kinds[k].help}</span>{/if}
					</span>
				</label>
				{#if k === 'diskLow'}
					<div class="ml-5.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted">
						Under
						<input class="input h-7 w-20 tabular-nums" type="number" min="1" max="10000" bind:value={diskGB} disabled={off.includes(k)} aria-label="Free space, in GB" />
						GB free
					</div>
				{:else if k === 'memoryHigh'}
					<div class="ml-5.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted">
						At least
						<input class="input h-7 w-16 tabular-nums" type="number" min="50" max="99" bind:value={memPct} disabled={off.includes(k)} aria-label="Memory in use, in percent" />
						% in use for
						<input class="input h-7 w-20 tabular-nums" type="number" min="1" max="1440" bind:value={memMinutes} disabled={off.includes(k)} aria-label="Minutes" />
						minutes
					</div>
				{/if}
			{/each}
		</fieldset>
		<div class="flex flex-wrap gap-2">
			<button class="btn btn-primary" disabled={!changed || !valid || busy}>Save</button>
			<button type="button" class="btn" disabled={!webhook.trim() || testing} onclick={test}><Icon name="send" size={14} /> Send a test</button>
		</div>
	</form>
{/if}

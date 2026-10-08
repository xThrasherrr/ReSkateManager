<script lang="ts">
	import { SvelteSet } from 'svelte/reactivity';
	import { uploads, type Upload } from '#lib/uploads.svelte.js';
	import { bytes } from '#lib/format.js';
	import Icon from './Icon.svelte';
	import Progress from './Progress.svelte';

	// Folded on phones, where the list would cover most of the page.
	let collapsed = $state(!matchMedia('(min-width: 40rem)').matches);
	const open = new SvelteSet<number>(); // failed uploads showing their error

	const total = $derived(uploads.list.length);
	const done = $derived(uploads.list.filter((u) => u.status === 'done').length);
	const failed = $derived(uploads.list.filter((u) => u.status === 'failed').length);
	const current = $derived(uploads.list.find((u) => u.status === 'uploading' || u.status === 'downloading' || u.status === 'installing'));
	const overall = $derived(total ? (done + failed + (current?.progress ?? 0)) / total : 0);

	const title = $derived(
		uploads.active
			? `Installing mods · ${done + failed + 1} of ${total}`
			: failed
				? `${done} installed, ${failed} failed`
				: `${done} mod${done === 1 ? '' : 's'} installed`
	);

	function status(u: Upload) {
		switch (u.status) {
			case 'queued':
				return 'Waiting';
			case 'uploading':
				return `${Math.round(u.progress * 100)}%`;
			case 'downloading':
				return u.progress ? `Downloading ${Math.round(u.progress * 100)}%` : 'Downloading…';
			case 'installing':
				return 'Installing…';
			case 'done':
				if (u.kind === 'update') return `Updated to v${u.version}`;
				if (u.kind === 'install') return `Installed v${u.version}`;
				return `${u.replaced ? 'Updated' : 'Added'} ${u.folder}`;
			case 'failed':
				return 'Failed';
		}
	}

	// A full reload would cut off an upload in flight, and drop the queue.
	// The manager carries on with a mod it is already downloading, and with
	// the updates it queued itself.
	function beforeunload(e: BeforeUnloadEvent) {
		if (uploads.list.some((u) => (u.status === 'queued' && !u.job) || u.status === 'uploading' || (u.kind === 'upload' && u.status === 'installing'))) e.preventDefault();
	}
</script>

<svelte:window onbeforeunload={beforeunload} />

{#if total}
	<section class="pointer-events-auto animate-slide-in overflow-hidden rounded-xl border-2 border-ink bg-raised text-sm shadow-sticker" aria-label="Mod uploads">
		<div class="flex items-center gap-1 py-1.5 pr-1.5 pl-3">
			<button class="flex min-w-0 flex-1 items-center gap-2 py-1 text-left" onclick={() => (collapsed = !collapsed)} aria-expanded={!collapsed}>
				<span
					class="grid size-5 shrink-0 place-items-center rounded-full border-2 border-ink text-ink {uploads.active ? 'bg-accent' : failed ? 'bg-bad' : 'bg-ok'}"
				>
					<Icon name={uploads.active ? 'upload' : failed ? 'alert' : 'check'} size={11} />
				</span>
				<span class="min-w-0 flex-1 truncate font-medium text-fg">{title}</span>
				<Icon name="chevron" size={14} class="text-faint transition-transform {collapsed ? 'rotate-180' : ''}" />
			</button>
			{#if !uploads.active}
				<button class="btn btn-ghost btn-sm size-7 px-0" onclick={() => uploads.clearFinished()} title="Clear" aria-label="Clear uploads"><Icon name="x" size={14} /></button>
			{/if}
		</div>

		{#if collapsed}
			{#if uploads.active}
				<Progress value={overall} label="All mod installs" size="thin" />
			{/if}
		{:else}
			<ul class="max-h-[min(20rem,40vh)] divide-y divide-line/60 overflow-y-auto border-t-2 border-line">
				{#each uploads.list as u (u.id)}
					{@const busy = u.status === 'uploading' || u.status === 'downloading' || u.status === 'installing'}
					<li class="space-y-1.5 px-3 py-2">
						<div class="flex items-center gap-2">
							<div class="min-w-0 flex-1">
								<div class="truncate text-fg" title={u.name}>{u.name}</div>
								<div class="truncate text-xs text-faint">
									{u.server} · {u.kind === 'upload' ? bytes(u.file?.size) : u.version ? `v${u.version} from Thunderstore` : 'from Thunderstore'}
								</div>
							</div>
							{#if u.status === 'failed'}
								<button
									class="flex shrink-0 items-center gap-1 text-xs font-medium text-bad hover:underline"
									onclick={() => (open.has(u.id) ? open.delete(u.id) : open.add(u.id))}
									aria-expanded={open.has(u.id)}
								>
									Failed <Icon name="chevron" size={12} class="transition-transform {open.has(u.id) ? 'rotate-180' : ''}" />
								</button>
							{:else}
								<span class="max-w-[45%] shrink-0 truncate text-xs tabular-nums {u.status === 'done' ? 'text-ok' : 'text-muted'}" title={status(u)}>{status(u)}</span>
							{/if}
							{#if uploads.canDismiss(u)}
								<button class="-m-1.5 grid size-7 shrink-0 place-items-center rounded-lg text-faint hover:bg-sunken hover:text-fg" onclick={() => uploads.dismiss(u.id)} aria-label="Remove {u.name}"
									><Icon name="x" size={12} /></button
								>
							{/if}
						</div>
						{#if u.status === 'queued' || busy}
							<Progress value={u.status === 'installing' ? null : u.status === 'queued' ? 0 : u.progress} label="{u.name}: {status(u)}" />
						{:else if u.status === 'failed' && open.has(u.id)}
							<div class="rounded-lg border-2 border-bad/40 bg-bad/5 px-2.5 py-2 text-xs">
								<p class="break-words whitespace-pre-wrap text-fg">{u.error}</p>
								{#if uploads.canRetry(u)}
									<button class="mt-1.5 font-medium text-accent hover:underline" onclick={() => uploads.retry(u.id)}>Try again</button>
								{/if}
							</div>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}
	</section>
{/if}

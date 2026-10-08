<script lang="ts">
	import { toast } from '#lib/toast.svelte.js';
	import type { Toast } from '#lib/toast.svelte.js';
	import Icon, { type IconName } from './Icon.svelte';
	const tone = { ok: 'bg-ok', error: 'bg-bad', info: 'bg-info' };
	const icon: Record<Toast['kind'], IconName> = { ok: 'check', error: 'alert', info: 'chat' };
</script>

<div class="flex flex-col gap-2">
	{#each toast.list as t (t.id)}
		<!-- Pointer or focus on any toast keeps them all up. -->
		<div
			class="pointer-events-auto flex animate-slide-in items-start gap-2.5 rounded-xl border-2 border-ink bg-raised py-1.5 pr-1.5 pl-3 text-sm shadow-sticker"
			role="group"
			aria-label={t.kind === 'error' ? 'Error' : 'Notice'}
			onmouseenter={() => toast.hold()}
			onmouseleave={() => toast.release()}
			onfocusin={() => toast.hold()}
			onfocusout={() => toast.release()}
		>
			<span class="mt-1 grid size-5 shrink-0 place-items-center rounded-full border-2 border-ink text-ink {tone[t.kind]}"><Icon name={icon[t.kind]} size={11} /></span>
			<span class="max-h-40 flex-1 overflow-y-auto py-1 break-words whitespace-pre-wrap text-fg">{t.text}</span>
			<button class="grid size-7 shrink-0 place-items-center rounded-lg text-muted hover:bg-sunken hover:text-fg" onclick={() => toast.dismiss(t.id)} aria-label="Dismiss"
				><Icon name="x" size={14} /></button
			>
		</div>
	{/each}
</div>

<!-- What screen readers hear: the text alone, errors at once. A new node each
     time, so the same words twice are said twice. -->
<div class="sr-only" aria-live="polite">{#key toast.said.polite.id}<span>{toast.said.polite.text}</span>{/key}</div>
<div class="sr-only" aria-live="assertive">{#key toast.said.assertive.id}<span>{toast.said.assertive.text}</span>{/key}</div>

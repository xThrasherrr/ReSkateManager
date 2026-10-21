<script lang="ts">
	import { page } from '$app/state';
	import Icon, { type IconName } from '#lib/components/Icon.svelte';

	interface Props {
		tabs: { href: string; label: string; icon: IconName }[];
	}
	let { tabs }: Props = $props();
	// The tab whose page this is, or holds it: /manager/plugins/x is under Plugins.
	const current = $derived(
		tabs
			.filter((t) => page.url.pathname === t.href || page.url.pathname.startsWith(t.href + '/'))
			.reduce<string | undefined>((best, t) => (t.href.length > (best?.length ?? -1) ? t.href : best), undefined)
	);

	// Keep the open tab in view when the strip scrolls sideways on a phone.
	let strip: HTMLElement | undefined = $state();
	$effect(() => {
		page.url.pathname;
		const el = strip?.querySelector<HTMLElement>('[aria-current="page"]');
		if (strip && el) strip.scrollTo({ left: el.offsetLeft - (strip.clientWidth - el.offsetWidth) / 2 });
	});
</script>

<nav bind:this={strip} class="no-scrollbar mt-3 flex gap-1 overflow-x-auto px-4 pb-3 text-sm sm:mt-4 sm:gap-1.5 sm:px-6 md:flex-wrap md:overflow-visible">
	{#each tabs as t (t.href)}
		{@const active = current === t.href}
		<a
			href={t.href}
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

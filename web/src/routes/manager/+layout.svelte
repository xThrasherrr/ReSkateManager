<script lang="ts">
	import { session } from '#lib/session.svelte.js';
	import TabStrip from '#lib/components/TabStrip.svelte';
	import type { IconName } from '#lib/components/Icon.svelte';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	const tabs: { href: string; label: string; icon: IconName }[] = [
		{ href: '/manager', label: 'General', icon: 'sliders' },
		{ href: '/manager/discord', label: 'Discord', icon: 'chat' },
		{ href: '/manager/plugins', label: 'Plugins', icon: 'package' },
		{ href: '/manager/backups', label: 'Backups', icon: 'archive' }
	];
</script>

<header class="border-b-2 border-ink bg-panel">
	<div class="px-4 pt-4 sm:px-6 sm:pt-5 {session.user?.owner ? '' : 'pb-4 sm:pb-5'}">
		<h1 class="page-title">Manager</h1>
		<p class="mt-2 text-sm text-muted">Settings for the manager itself. Changes apply right away.</p>
	</div>
	{#if session.user?.owner}<TabStrip {tabs} />{/if}
</header>
<!-- Pages lay out by this box's width, which the sidebar changes, not the window's. -->
<div class="@container max-w-[100rem] p-4 sm:p-6">
	{#if session.user?.owner}
		{@render children()}
	{:else}
		<p class="text-sm text-muted">Only owners can change the manager's settings.</p>
	{/if}
</div>

<script lang="ts">
	import { session } from '#lib/session.svelte.js';
	import AccessSettings from '#lib/components/AccessSettings.svelte';
	import AlertSettings from '#lib/components/AlertSettings.svelte';
	import BackupSettings from '#lib/components/BackupSettings.svelte';
	import DiscordSettings from '#lib/components/DiscordSettings.svelte';
	import RetentionSettings from '#lib/components/RetentionSettings.svelte';
</script>

<div class="mx-auto max-w-2xl space-y-4 p-4 sm:space-y-6 sm:p-6">
	<div>
		<h1 class="page-title">Manager</h1>
		<p class="mt-2 text-sm text-muted">Settings for the manager itself. Changes apply right away.</p>
	</div>
	{#if session.user?.owner}
		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">Access</h2>
			<AccessSettings />
		</section>
		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">Discord alerts</h2>
			<AlertSettings />
		</section>
		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">Discord status message</h2>
			<DiscordSettings />
		</section>
		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">Backups</h2>
			<BackupSettings />
		</section>
		<section class="card p-4 sm:p-5">
			<h2 class="mb-3">Housekeeping</h2>
			<RetentionSettings />
		</section>
	{:else}
		<p class="text-sm text-muted">Only owners can change the manager's settings.</p>
	{/if}
</div>

<script lang="ts">
	import AlertSettings from '#lib/components/AlertSettings.svelte';
	import DiscordSettings from '#lib/components/DiscordSettings.svelte';
</script>

<div class="grid items-start gap-4 sm:gap-6 @5xl:grid-cols-2">
	<section class="card p-4 sm:p-5">
		<h2 class="mb-3">Discord alerts</h2>
		<AlertSettings />
	</section>
	<section class="card p-4 sm:p-5">
		<h2 class="mb-3">Discord status message</h2>
		<DiscordSettings />
	</section>
</div>

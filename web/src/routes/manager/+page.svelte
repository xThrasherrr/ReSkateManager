<script lang="ts">
	import AccessSettings from '#lib/components/AccessSettings.svelte';
	import RetentionSettings from '#lib/components/RetentionSettings.svelte';
</script>

<div class="grid items-start gap-4 sm:gap-6 @5xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
	<section class="card p-4 sm:p-5">
		<h2 class="mb-3">Access</h2>
		<AccessSettings />
	</section>
	<section class="card p-4 sm:p-5">
		<h2 class="mb-3">Housekeeping</h2>
		<RetentionSettings />
	</section>
</div>

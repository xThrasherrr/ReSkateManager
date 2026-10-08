<script lang="ts">
	import { page } from '$app/state';

	const missing = $derived(page.status === 404);
</script>

<div class="mx-auto max-w-md p-6 text-center">
	<h1 class="page-title">{missing ? 'Nothing here' : 'Something went wrong'}</h1>
	<p class="mt-2 text-sm text-muted">{missing ? 'The panel has no page at that address.' : (page.error?.message ?? 'The page failed to load.')}</p>
	<a href="/" class="btn mt-4">Back to the dashboard</a>
</div>

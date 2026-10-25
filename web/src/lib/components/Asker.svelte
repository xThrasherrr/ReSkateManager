<script lang="ts">
	import { asker } from '#lib/ask.svelte.js';
	import Modal from './Modal.svelte';

	const q = $derived(asker.question);
</script>

<!-- Closed any way but the action, by Cancel or Escape, is a no. -->
<Modal bind:open={() => !!q, (open) => !open && asker.answer(false)} title={q?.title ?? ''}>
	<p class="text-sm text-muted">{q?.body}</p>
	{#if q?.tick}
		<label class="mt-3 inline-flex cursor-pointer items-center gap-2">
			<input type="checkbox" class="peer sr-only" bind:checked={asker.ticked} />
			<span class="switch"></span>
			<span class="text-sm text-muted">{q.tick}</span>
		</label>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => asker.answer(false)}>Cancel</button>
		<button class="btn {q?.danger === false ? 'btn-primary' : 'btn-danger'}" onclick={() => asker.answer(true)}>{q?.action}</button>
	{/snippet}
</Modal>

<script lang="ts">
	import { asker } from '#lib/ask.svelte.js';
	import Modal from './Modal.svelte';

	const q = $derived(asker.question);
</script>

<!-- Closed any way but the action, by Cancel or Escape, is a no. -->
<Modal bind:open={() => !!q, (open) => !open && asker.answer(false)} title={q?.title ?? ''}>
	<p class="text-sm text-muted">{q?.body}</p>
	{#snippet actions()}
		<button class="btn" onclick={() => asker.answer(false)}>Cancel</button>
		<button class="btn {q?.danger === false ? 'btn-primary' : 'btn-danger'}" onclick={() => asker.answer(true)}>{q?.action}</button>
	{/snippet}
</Modal>

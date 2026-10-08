<script lang="ts">
	// What a part of a page shows until its first answer comes.
	let { text = 'loading…', class: klass = '' }: { text?: string; class?: string } = $props();
</script>

<p class="font-marker text-sm text-faint {klass}" role="status">{text}</p>

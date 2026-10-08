<script lang="ts">
	import Icon from './Icon.svelte';

	// A Mods page's buttons: browse Thunderstore, read the list again, upload zips.
	let {
		browse,
		busy,
		canUpload,
		uploading,
		onrefresh,
		onfiles
	}: { browse: string; busy: boolean; canUpload: boolean; uploading: boolean; onrefresh: () => void; onfiles: (files: FileList | null) => void } = $props();

	let picker = $state<HTMLInputElement>();
</script>

<a class="btn" href={browse}><Icon name="search" size={14} /> Browse mods</a>
<button class="btn" disabled={busy} onclick={onrefresh} title="Reload the list and check Thunderstore for updates"><Icon name="restart" size={14} /> Refresh</button>
{#if canUpload}
	<input
		bind:this={picker}
		type="file"
		accept=".zip,application/zip"
		multiple
		hidden
		onchange={(e) => {
			onfiles(e.currentTarget.files);
			e.currentTarget.value = '';
		}}
	/>
	<button class="btn btn-primary" onclick={() => picker?.click()}>
		<Icon name="upload" size={14} />
		{uploading ? 'Upload more' : 'Upload mod'}
	</button>
{/if}

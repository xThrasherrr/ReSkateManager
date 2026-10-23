<script lang="ts">
	import { onMount } from 'svelte';
	import type { EditorView } from '@codemirror/view';

	// One file in CodeMirror. It takes text as it mounts, so key it by the file.
	let { path, text, onchange, readonly = false }: { path: string; text: string; onchange: (text: string) => void; readonly?: boolean } = $props();
	let host = $state<HTMLDivElement>();

	onMount(() => {
		let view: EditorView | undefined;
		let gone = false;
		import('#lib/codemirror.js').then(({ editor }) => {
			if (!gone && host) view = editor(host, text, path, onchange, readonly);
		});
		return () => {
			gone = true;
			view?.destroy();
		};
	});
</script>

<div bind:this={host} class="h-full"></div>

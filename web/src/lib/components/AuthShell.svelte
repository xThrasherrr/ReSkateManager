<script lang="ts">
	import type { Snippet } from 'svelte';
	let { tagline, subtitle, wide = false, children }: { tagline: string; subtitle: string; wide?: boolean; children: Snippet } = $props();
</script>

<div class="relative grid min-h-dvh place-items-center overflow-hidden px-4 py-10">
	<div class="halftone pointer-events-none absolute -top-24 -right-24 size-[30rem] opacity-40 [mask-image:radial-gradient(circle,black,transparent_70%)]"></div>
	<div
		class="halftone pointer-events-none absolute -bottom-32 -left-32 size-[34rem] opacity-30 [--color-accent:var(--color-pink)] [mask-image:radial-gradient(circle,black,transparent_70%)]"
	></div>

	<div class="relative w-full {wide ? 'max-w-md' : 'max-w-sm'}">
		<div class="mb-7 flex flex-col items-center text-center">
			<h1><img src="/logo.png" alt="ReSkateManager" class="w-56" /></h1>
			<p class="mt-2 -rotate-2 font-marker text-lg text-warn">{tagline}</p>
			<p class="mt-3 text-sm text-muted">{subtitle}</p>
		</div>
		<div class="relative">
			<!-- A strip of tape holding the card down. -->
			<div class="absolute -top-3 left-1/2 z-10 h-6 w-28 -translate-x-1/2 rotate-[-4deg] rounded-sm bg-warn/85 shadow-ledge-sm [clip-path:polygon(3%_0,97%_4%,100%_50%,97%_100%,2%_96%,0_50%)]"></div>
			{@render children()}
		</div>
	</div>
</div>

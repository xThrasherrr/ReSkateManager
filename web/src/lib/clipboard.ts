import { toast } from './toast.svelte';

/**
 * Copies text and says so. Browsers only offer the Clipboard API on https or
 * localhost, so a panel opened on a LAN address falls back to a hidden textarea.
 */
export async function copy(text: string) {
	try {
		if (navigator.clipboard) await navigator.clipboard.writeText(text);
		else legacyCopy(text);
		toast.ok('Copied');
	} catch {
		toast.error('Could not copy. Select the text and copy it instead.');
	}
}

function legacyCopy(text: string) {
	const area = document.createElement('textarea');
	area.value = text;
	area.setAttribute('readonly', '');
	area.style.position = 'fixed';
	area.style.opacity = '0';
	document.body.append(area);
	area.select();
	try {
		if (!document.execCommand('copy')) throw new Error('copy refused');
	} finally {
		area.remove();
	}
}

import { beforeNavigate, goto } from '$app/navigation';
import { ask } from './ask.svelte';

/**
 * Keeps edits from being lost by leaving the page while unsaved() says they
 * are: within the panel it asks first, and closing or reloading the tab gets
 * the browser's own warning. Call it while the component starts.
 */
export function guardUnsaved(unsaved: () => boolean, body = "Your changes here aren't saved yet; leaving drops them.") {
	let leaving = false;
	beforeNavigate((nav) => {
		if (leaving || !unsaved()) return;
		nav.cancel(); // for a closing tab, this is what makes the browser ask
		if (nav.type === 'leave') return;
		const to = nav.to?.url;
		const delta = nav.type === 'popstate' ? nav.delta : 0;
		ask({ title: 'Leave without saving?', body, action: 'Leave' }).then((go) => {
			if (!go) return;
			leaving = true;
			if (delta) history.go(delta);
			else if (to) goto(to);
		});
	});
}

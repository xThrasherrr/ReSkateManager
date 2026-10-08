import type { HandleClientError } from '@sveltejs/kit/hooks';

// The browsers' words for a page's code that couldn't be fetched: Chrome,
// Firefox, Safari.
const notFetched = /dynamically imported module|Importing a module script failed/i;

/** What the error page says about an error nobody expected. A page whose code
 * couldn't be fetched says why: the manager restarting, or the connection gone. */
export const handleError: HandleClientError = ({ kind, error }) => {
	if (kind !== 'unknown') return;
	console.error(error);
	if (error instanceof TypeError && notFetched.test(error.message))
		return { message: "This page couldn't be loaded from the manager. Check the connection, and that the manager is running, then reload." };
	return { message: 'The page failed to load. Reload to try again.' };
};

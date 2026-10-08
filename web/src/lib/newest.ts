/**
 * Keeps a page to its newest load: an answer to an older one, such as a slow
 * one for the filter just changed, is dropped. Call what it returns as a load
 * starts; the function that gives back says, once the answer is in, whether
 * that load is still the newest.
 */
export function newest() {
	let n = 0;
	return () => {
		const mine = ++n;
		return () => mine === n;
	};
}

/**
 * Which of a page's actions is running, so its buttons wait for it rather
 * than send it twice. One at a time per page: a second click while any runs
 * does nothing.
 */
export class Busy {
	/** The running action's key, or '' when none is. */
	now = $state('');

	/** Runs fn as action key, unless an action is running already; then it
	 * returns undefined without running fn. */
	async run<T>(key: string, fn: () => Promise<T>): Promise<T | undefined> {
		if (this.now) return undefined;
		this.now = key;
		try {
			return await fn();
		} finally {
			this.now = '';
		}
	}

	/** Whether action key is running; with no key, whether any is. */
	is = (key?: string) => (key === undefined ? this.now !== '' : this.now === key);
}

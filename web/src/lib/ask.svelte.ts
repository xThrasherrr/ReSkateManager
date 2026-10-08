export interface Question {
	title: string;
	body: string;
	/** The button that goes ahead, such as "Stop". */
	action: string;
	/** Red, for what can't be taken back or cuts players off; the default. */
	danger?: boolean;
}

/** The one question on screen, answered by the dialog in the root layout. */
class Asker {
	question = $state.raw<Question | null>(null);
	#resolve: ((yes: boolean) => void) | null = null;

	ask(q: Question): Promise<boolean> {
		this.#resolve?.(false); // a question still open counts as a no
		this.question = q;
		return new Promise((resolve) => (this.#resolve = resolve));
	}

	answer(yes: boolean) {
		const resolve = this.#resolve;
		this.#resolve = null;
		this.question = null;
		resolve?.(yes);
	}
}

export const asker = new Asker();

/** Asks before something that can't be taken back, or that cuts players off.
 * Resolves to whether they went ahead; closing the dialog is a no. */
export const ask = (q: Question) => asker.ask(q);

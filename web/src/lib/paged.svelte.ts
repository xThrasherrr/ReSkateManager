/** How many rows a list may show at once. */
export const pageSizes = [10, 20, 50, 100] as const;
export type PageSize = (typeof pageSizes)[number];

/**
 * A long list shown a page at a time. How many rows a page has is the
 * viewer's choice for each kind of list, kept in this browser.
 */
export class Paged<T> {
	size = $state<PageSize>(pageSizes[0]);
	#page = $state(1);
	#items: () => T[];
	#key: string;

	constructor(key: string, items: () => T[]) {
		this.#items = items;
		this.#key = `rsm.pageSize.${key}`;
		try {
			const n = Number(localStorage.getItem(this.#key));
			if ((pageSizes as readonly number[]).includes(n)) this.size = n as PageSize;
		} catch {
			// No storage here: the default.
		}
	}

	get total() {
		return this.#items().length;
	}
	get pages() {
		return Math.max(1, Math.ceil(this.total / this.size));
	}
	/** The page shown: the one asked for, or the last there is once the list got shorter. */
	get page() {
		return Math.min(this.#page, this.pages);
	}
	set page(n: number) {
		this.#page = Math.min(Math.max(1, n), this.pages);
	}
	/** The rows of the page, counted from 1. */
	get from() {
		return this.total ? (this.page - 1) * this.size + 1 : 0;
	}
	get to() {
		return Math.min(this.page * this.size, this.total);
	}
	get shown(): T[] {
		return this.#items().slice((this.page - 1) * this.size, this.page * this.size);
	}

	resize(size: PageSize) {
		this.size = size;
		this.#page = 1;
		try {
			localStorage.setItem(this.#key, String(size));
		} catch {
			// Not kept: it lasts until the page is left.
		}
	}
}

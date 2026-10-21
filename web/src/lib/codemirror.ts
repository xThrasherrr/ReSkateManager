// The plugin editor's CodeMirror, imported only by the page that opens it.
import { basicSetup } from 'codemirror';
import { EditorView, keymap } from '@codemirror/view';
import { indentWithTab } from '@codemirror/commands';
import { HighlightStyle, StreamLanguage, indentUnit, syntaxHighlighting } from '@codemirror/language';
import { lua } from '@codemirror/legacy-modes/mode/lua';
import { toml } from '@codemirror/legacy-modes/mode/toml';
import { tags as t } from '@lezer/highlight';

// The panel's own colours, so the editor follows app.css.
const theme = EditorView.theme(
	{
		'&': { height: '100%', color: 'var(--color-fg)', backgroundColor: 'var(--color-sunken)', fontSize: '13px' },
		'.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.6' },
		'.cm-content': { caretColor: 'var(--color-accent)' },
		'.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--color-accent)', borderLeftWidth: '2px' },
		'&.cm-focused': { outline: 'none' },
		'&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
			backgroundColor: 'color-mix(in srgb, var(--color-accent) 28%, transparent)'
		},
		'.cm-gutters': { color: 'var(--color-faint)', backgroundColor: 'var(--color-sunken)', border: 'none' },
		'.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--color-raised) 55%, transparent)' },
		'.cm-activeLineGutter': { color: 'var(--color-fg)', backgroundColor: 'transparent' },
		'.cm-matchingBracket, &.cm-focused .cm-matchingBracket': { color: 'var(--color-fg)', backgroundColor: 'var(--color-line-strong)' },
		'.cm-selectionMatch': { backgroundColor: 'color-mix(in srgb, var(--color-info) 22%, transparent)' },
		'.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--color-warn) 30%, transparent)' },
		'.cm-foldPlaceholder': { color: 'var(--color-muted)', backgroundColor: 'var(--color-raised)', border: 'none' },
		'.cm-panels': { color: 'var(--color-fg)', backgroundColor: 'var(--color-panel)' },
		'.cm-panels-bottom': { borderTop: '2px solid var(--color-line)' },
		'.cm-panel input, .cm-panel button': { font: 'inherit' },
		'.cm-tooltip': { color: 'var(--color-fg)', backgroundColor: 'var(--color-raised)', border: '2px solid var(--color-line)', borderRadius: '8px' },
		'.cm-tooltip-autocomplete > ul > li[aria-selected]': { color: 'var(--color-ink)', backgroundColor: 'var(--color-accent)' }
	},
	{ dark: true }
);

const highlight = HighlightStyle.define([
	{ tag: t.keyword, color: 'var(--color-pink)' },
	{ tag: t.string, color: 'var(--color-ok)' },
	{ tag: [t.number, t.atom, t.bool], color: 'var(--color-warn)' },
	{ tag: t.comment, color: 'var(--color-faint)', fontStyle: 'italic' },
	{ tag: [t.standard(t.variableName), t.propertyName], color: 'var(--color-accent)' },
	{ tag: [t.special(t.variableName), t.heading], color: 'var(--color-info)' }
]);

const languages: Record<string, StreamLanguage<unknown>> = {
	lua: StreamLanguage.define(lua),
	toml: StreamLanguage.define(toml)
};

/** An editor for one file in parent, telling onchange each edit. */
export function editor(parent: HTMLElement, doc: string, path: string, onchange: (text: string) => void): EditorView {
	const lang = languages[path.slice(path.lastIndexOf('.') + 1).toLowerCase()];
	return new EditorView({
		parent,
		doc,
		extensions: [
			basicSetup,
			keymap.of([indentWithTab]), // Esc, then Tab, still leaves the editor
			indentUnit.of('  '),
			theme,
			syntaxHighlighting(highlight),
			lang ?? [],
			EditorView.contentAttributes.of({ 'aria-label': path }),
			EditorView.updateListener.of((u) => {
				if (u.docChanged) onchange(u.state.doc.toString());
			})
		]
	});
}

// What xf web's editor (the Scratch view, the source pane and the settings page's JSON view) needs of CodeMirror 6, bundled by `npm run build`.
export { EditorState, Text, Compartment, Prec, StateField, StateEffect, ChangeSet, RangeSet } from '@codemirror/state';
export { EditorView, Decoration, WidgetType, ViewPlugin, gutter, GutterMarker, gutterLineClass, hoverTooltip, closeHoverTooltips, showTooltip, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, placeholder } from '@codemirror/view';
export { history, historyKeymap, defaultKeymap, indentWithTab } from '@codemirror/commands';
export { sql, SQLDialect, StandardSQL } from '@codemirror/lang-sql';
export { json } from '@codemirror/lang-json';
export { syntaxHighlighting, HighlightStyle, bracketMatching, indentOnInput, StreamLanguage, syntaxTree } from '@codemirror/language';
export { javascript as javascriptMode, json as jsonMode } from '@codemirror/legacy-modes/mode/javascript';
export { yaml as yamlMode } from '@codemirror/legacy-modes/mode/yaml';
export { toml as tomlMode } from '@codemirror/legacy-modes/mode/toml';
export { python as pythonMode } from '@codemirror/legacy-modes/mode/python';
export { shell as shellMode } from '@codemirror/legacy-modes/mode/shell';
export { highlightSelectionMatches, searchKeymap } from '@codemirror/search';
export { tags } from '@lezer/highlight';
export { vim, Vim, getCM } from '@replit/codemirror-vim';
export { autocompletion, startCompletion, acceptCompletion, completionKeymap, closeCompletion, moveCompletionSelection, completionStatus, snippetCompletion, hasNextSnippetField, hasPrevSnippetField } from '@codemirror/autocomplete';
export { setDiagnostics, forEachDiagnostic } from '@codemirror/lint';
export { Chunk } from '@codemirror/merge';

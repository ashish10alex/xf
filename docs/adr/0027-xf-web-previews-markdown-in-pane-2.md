---
status: accepted
---

# xf web previews Markdown in pane 2, rendered by goldmark on the server

Beside a Markdown file (`.md`, `.markdown`) in the source pane, pane 2 shows the file rendered, in place of the actions, run and SQL panes, which have nothing to show for a file with no action. The page sends the editor's text to `POST /api/markdown` a moment (150 ms) after each edit, so the preview follows unsaved changes. The server renders it with [goldmark](https://github.com/yuin/goldmark) and GitHub's extensions (tables, task lists, strikethrough, bare links), and colours code blocks with chroma, as the SQL pane colours SQL. Raw HTML in the file and `javascript:` links are left out. Headings get GitHub's ids with `md-` in front, so they can't clash with the page's own ids. A link to a file of the project opens it in the source pane, a web link opens in the browser, and an image of the project loads from `GET /api/markdown/image`. That route serves image types only, sandboxed so that an SVG opened on its own runs no script. `autoHidePane2` leaves pane 2 shown beside a Markdown file. `markdown.js` and `markdown-view.js` load with the first Markdown file, outside the first load.

## Why

The logic stays in Go and the page draws what it is sent (ADR 0004). goldmark is CommonMark-compliant, has no dependencies and is the usual Go choice. chroma is already in xf, so code blocks match the SQL pane. Rendering on the server adds nothing to the page's first load (`TestFirstLoadBudget`).

## Considered options

- **software-mansion/enriched-markdown.** It is a React Native library, and its web build needs React and React Native's web layer. xf web is Preact with no build step, so it would bring in a second UI framework and break the first-load budget. Its strengths, such as streaming AI chat output and rich text input, aren't what a read-only preview needs.
- **A JavaScript renderer in the page (marked, markdown-it).** It would be about 40 KB more for the page to load. The page would have to strip unsafe HTML itself, and code blocks wouldn't match chroma's colours.
- **A toggle in the source pane between the text and the preview.** You couldn't see what you type and what it renders to at once.

## Consequences

- The preview replaces the whole pane 2. To see an action's panes again, select an action.
- The preview doesn't follow the editor's scroll.

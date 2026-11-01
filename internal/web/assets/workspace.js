// A tab's workspace for each project it has shown (ADR 0008): what the
// page showed and where, kept in the tab's sessionStorage as the page
// goes (a switch to another project, a reload) and put back when the tab
// shows the project again. Each module adds a part: save answers what
// JSON can hold (light: without the big things, undo history and query
// results, when storage is short), and restore is given it back.
//
// The workspace is the tab's own: another tab, or this one once closed,
// starts from the session's selection. A project closing keeps it, so
// unsaved text in it is not lost.

const prefix = 'xf-web-ws:';
const unsavedKey = 'xf-web-unsaved';
// big is the size above which a workspace is saved light.
const big = 1.5e6;

const parts = new Map();

export function part(name, save, restore) { parts.set(name, { save, restore }); }

// root is the project this page shows, '' on the projects.
let root = '';
export function setWorkspaceRoot(r) { root = r; }

function collect(light) {
  const out = { v: 1, at: Date.now(), parts: {} };
  for (const [name, p] of parts) {
    try {
      const v = p.save(light);
      if (v != null) out.parts[name] = v;
    } catch (e) { console.warn('xf: saving the ' + name + ' workspace:', e); }
  }
  return out;
}

// saveWorkspace keeps this page's workspace for its project.
export function saveWorkspace() {
  if (!root) return;
  let ws = collect(false);
  let text = JSON.stringify(ws);
  if (text.length > big) { ws = collect(true); text = JSON.stringify(ws); }
  if (!store(prefix + root, text)) {
    // Full: the other projects' workspaces go, the oldest first.
    for (const k of others()) {
      try { sessionStorage.removeItem(k); } catch { /* gone */ }
      if (store(prefix + root, text)) break;
    }
  }
  const src = ws.parts.source;
  setUnsaved(root, src && src.dirty ? [src.file] : []);
}

function store(k, text) {
  try { sessionStorage.setItem(k, text); return true; } catch { return false; }
}

// others are the keys of the other projects' workspaces, the oldest first.
function others() {
  const out = [];
  try {
    for (let i = 0; i < sessionStorage.length; i++) {
      const k = sessionStorage.key(i);
      if (k && k.startsWith(prefix) && k !== prefix + root) {
        let at = 0;
        try { at = JSON.parse(sessionStorage.getItem(k)).at || 0; } catch { /* oldest */ }
        out.push([at, k]);
      }
    }
  } catch { /* no storage */ }
  return out.sort((a, b) => a[0] - b[0]).map((x) => x[1]);
}

// workspaceOf is the workspace kept for the project at r, null for none.
export function workspaceOf(r) {
  try { return JSON.parse(sessionStorage.getItem(prefix + r) || 'null'); } catch { return null; }
}

// restoreParts gives each part (but those named in skip) what it saved.
export function restoreParts(ws, skip = []) {
  if (!ws || !ws.parts) return;
  for (const [name, p] of parts) {
    if (skip.includes(name) || !p.restore || ws.parts[name] == null) continue;
    try { p.restore(ws.parts[name]); } catch (e) { console.warn('xf: restoring the ' + name + ' workspace:', e); }
  }
}

// ---- unsaved files ----

// The tab's workspaces may hold unsaved text of projects it no longer
// shows; it tells xf web which files, with the live ones of the project
// it shows, so every tab can mark them.

function readUnsaved() {
  try { const m = JSON.parse(sessionStorage.getItem(unsavedKey) || '{}'); return m && typeof m === 'object' ? m : {}; } catch { return {}; }
}

function setUnsaved(r, files) {
  const m = readUnsaved();
  if (files.length) m[r] = files; else delete m[r];
  store(unsavedKey, JSON.stringify(m));
}

// unsavedNow is the tab's unsaved files by project root: those its
// workspaces keep, and live, those of the page's own project now.
export function unsavedNow(live) {
  const m = readUnsaved();
  if (root) { if (live.length) m[root] = live; else delete m[root]; }
  return m;
}

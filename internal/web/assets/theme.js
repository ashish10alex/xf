// xf web's colour theme and its picker. The colours are themes.css's,
// built from internal/webtheme: the page shows a theme by setting its id
// as <html data-theme>. The saved choice is the server's (webTheme and
// the light and dark themes auto shows); this page keeps a copy so the
// next load shows it before the server answers.

// T is the theme state: choice is { theme, light, dark } as the server
// has it, own the theme keys the project's own config file sets (they win
// over the user's), themes every theme ({ id, name, kind }), and pick the
// open picker, or null.
export const T = {
  choice: { theme: 'auto', light: 'xf-light', dark: 'xf-dark' },
  own: [],
  themes: [],
  pick: null,
};

// api is what state.js gives: post, redraw, say and inProject.
let api = null;
export function themeHooks(h) { api = h; }

// The saved choice is a project's own when its file sets one, so each
// project keeps its copy.
const storeKey = 'xf.theme:' + (new URLSearchParams(location.search).get('project') || '');
const darkScheme = window.matchMedia('(prefers-color-scheme: dark)');

// resolve is the id of the theme choice shows: auto's light or dark one
// as the browser's colour scheme is.
const resolve = (c) => (c.theme !== 'auto' ? c.theme : darkScheme.matches ? c.dark : c.light);

function show(id) { document.documentElement.setAttribute('data-theme', id); }

// The theme this browser last showed, before the server says.
try {
  const c = JSON.parse(localStorage.getItem(storeKey) || 'null');
  if (c && c.theme) { T.choice = c; T.own = c.own || []; show(resolve(c)); }
} catch { /* themes.css's default: xf's own */ }

darkScheme.addEventListener('change', () => {
  if (T.choice.theme === 'auto' && !T.pick) { show(resolve(T.choice)); if (api) api.redraw(); }
});

// applyTheme shows choice, the server's, and keeps it for the next load;
// themes, when given, are the themes the picker lists, and own the keys
// the project's file sets.
export function applyTheme(choice, themes, own) {
  if (themes) T.themes = themes;
  if (!choice || !choice.theme) return;
  T.choice = { theme: choice.theme, light: choice.light, dark: choice.dark };
  T.own = own || [];
  if (!T.pick) show(resolve(T.choice));
  try { localStorage.setItem(storeKey, JSON.stringify({ ...T.choice, own: T.own })); } catch { /* the next load waits for the server */ }
}

// themeName is the name of the theme id, or id.
export function themeName(id) {
  const t = T.themes.find((x) => x.id === id);
  return t ? t.name : id;
}

// themeLabel is what the ◐ button says: the theme shown, after "auto · "
// when auto picked it.
export function themeLabel() {
  const name = themeName(resolve(T.choice));
  return (T.choice.theme === 'auto' ? 'auto · ' + name : name) + (T.own.length ? ' · project' : '');
}

// slots are the pickers: the theme, and the light and dark ones auto
// shows, each with its config key and title.
const slots = {
  theme: { key: 'webTheme', title: 'colour theme' },
  light: { key: 'webThemeLight', title: 'light theme for auto' },
  dark: { key: 'webThemeDark', title: 'dark theme for auto' },
};

// openThemePicker opens the picker of slot (theme, light or dark), its
// cursor on the theme it has.
export function openThemePicker(slot) {
  slot = slots[slot] ? slot : 'theme';
  // In a project the pick saves for this project when its file already
  // sets a theme, else for every project; tab changes it.
  T.pick = { slot, title: slots[slot].title, query: '', cursor: 0, scope: canScope() && T.own.length ? 'project' : 'user' };
  const rows = themeRows();
  T.pick.cursor = Math.max(rows.findIndex((r) => r.id === T.choice[slot]), 0);
  preview();
}

// canScope says the picker can save for the project alone: a project's
// page has one.
export function canScope() { return !!(api && api.inProject && api.inProject()); }

// toggleScope flips the picker between saving for every project and for
// this one.
export function toggleScope() {
  const p = T.pick;
  if (!p || !canScope()) return;
  p.scope = p.scope === 'project' ? 'user' : 'project';
  p.cursor = Math.min(p.cursor, Math.max(themeRows().length - 1, 0));
  preview();
}

// themeRows are the picker's rows its query matches: auto then every
// theme for the theme, the themes of one kind for light and dark. Each
// says which of the choice's slots it fills.
export function themeRows() {
  const p = T.pick;
  if (!p) return [];
  const c = T.choice;
  const rows = [];
  if (p.slot === 'theme' && !p.auto && p.scope === 'project' && T.own.length) rows.push({ id: 'inherit', name: 'inherit', kind: '', note: 'drop this project\'s theme: use the one for every project' });
  if (p.slot === 'theme') rows.push({ id: 'auto', name: 'auto', kind: '', note: themeName(c.light) + ' · ' + themeName(c.dark) });
  for (const t of T.themes) {
    if (p.slot !== 'theme' && t.kind !== p.slot) continue;
    const marks = [];
    if (c.theme === t.id) marks.push('current');
    if (t.id === c.light) marks.push('light for auto');
    if (t.id === c.dark) marks.push('dark for auto');
    rows.push({ ...t, note: marks.join(' · ') });
  }
  const words = p.query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return rows;
  return rows.filter((r) => words.every((w) => (r.name + ' ' + r.id + ' ' + r.kind).toLowerCase().includes(w)));
}

// preview shows the theme under the cursor, until the picker closes.
function preview() {
  const r = themeRows()[T.pick.cursor];
  if (!r) return;
  if (r.id === 'inherit') show(resolve(T.choice));
  else show(r.id === 'auto' ? resolve({ ...T.choice, theme: 'auto' }) : r.id);
}

export function setThemeQuery(q) {
  T.pick.query = q;
  T.pick.cursor = 0;
  preview();
}

export function moveTheme(n) {
  const rows = themeRows();
  T.pick.cursor = Math.min(Math.max(T.pick.cursor + n, 0), Math.max(rows.length - 1, 0));
  preview();
}

// hoverTheme puts the cursor on row i, reporting whether it moved.
export function hoverTheme(i) {
  if (T.pick.cursor === i) return false;
  T.pick.cursor = i;
  preview();
  return true;
}

// closeThemePicker closes the picker, showing the theme chosen.
export function closeThemePicker() {
  T.pick = null;
  show(resolve(T.choice));
}

// pickTheme keeps the theme under the cursor (or i's) for the picker's
// slot: it shows at once, and the server saves it and tells the other
// tabs. Picking auto goes on to ask for auto's light theme and then its
// dark one, each previewed as the cursor moves; the three are saved once
// the dark one is picked, and esc on the way keeps what was saved.
export function pickTheme(i) {
  const p = T.pick;
  const r = themeRows()[i === undefined ? p.cursor : i];
  if (!r) return;
  const scope = p.scope === 'project' ? 'project' : undefined;
  if (r.id === 'inherit') {
    T.pick = null;
    show(resolve(T.choice));
    dropOwn();
    return;
  }
  if (p.slot === 'theme' && r.id === 'auto') { autoStep('light', {}, p.scope); return; }
  if (p.auto) {
    const pair = { ...p.auto, [p.slot]: r.id };
    if (p.slot === 'light') { autoStep('dark', pair, p.scope); return; }
    T.pick = null;
    saveAll({ theme: 'auto', light: pair.light, dark: pair.dark }, scope);
    return;
  }
  const { key } = slots[p.slot];
  T.pick = null;
  applyTheme({ ...T.choice, [p.slot]: r.id }, null, scope ? [...new Set([...T.own, key])] : T.own);
  api.post('/api/theme', { key, theme: r.id, scope }).then((res) => { if (res && res.message) api.say(res.message); }).catch((e) => api.say(e.message));
}

// autoStep is auto's picker of slot (light, then dark), its cursor on
// the theme saved for it; pair holds what the steps before picked.
function autoStep(slot, pair, scope) {
  T.pick = { slot, title: 'auto · ' + slot + ' theme (' + (slot === 'light' ? 1 : 2) + ' of 2)', query: '', cursor: 0, auto: pair, scope };
  T.pick.cursor = Math.max(themeRows().findIndex((r) => r.id === T.choice[slot]), 0);
  preview();
}

// saveAll keeps choice, auto and its two themes: it shows at once, and
// the server saves each key, for scope ('project', or nothing for every
// project).
async function saveAll(choice, scope) {
  applyTheme(choice, null, scope ? Object.values(slots).map((x) => x.key) : T.own);
  try {
    let res = null;
    for (const [slot, key] of [['light', 'webThemeLight'], ['dark', 'webThemeDark'], ['theme', 'webTheme']]) {
      res = await api.post('/api/theme', { key, theme: choice[slot], scope });
    }
    if (res && res.message) api.say(res.message);
  } catch (e) { api.say(e.message); }
}

// dropOwn takes the project's theme keys out of its file, so the theme of
// every project applies to it again; the server tells the page which.
async function dropOwn() {
  try {
    for (const key of T.own) await api.post('/api/theme', { key, theme: '', scope: 'project' });
  } catch (e) { api.say(e.message); }
}

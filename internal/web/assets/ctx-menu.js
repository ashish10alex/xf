// A right-click menu at a point on the page, as the source pane's
// (source-menu.js) and the file tree's (tree.js) are. One shows at a time.
//
// Each item is {label, key, off, run}: key is the key that does the same,
// off why it can't be done (the row is dimmed and shows it). A '-'
// separates groups. While it shows, the menu has every key but cmd/ctrl
// ones (copy, the palette…), which close it and go on.

import { redraw } from './state.js';

let menu = null; // the open menu: {el, rows, at, onPick, close()}

export const menuOpen = () => !!menu;

// showMenu shows items at xy (client px). opts.scrollEl closes it when
// that element scrolls; opts.onPick runs before a picked item does (the
// source pane gives its editor focus back there).
export function showMenu(items, xy, opts = {}) {
  closeMenu();
  const el = document.createElement('div');
  el.className = 'ctx-menu';
  el.setAttribute('role', 'menu');
  const rows = [];
  items.forEach((it) => {
    if (it === '-') {
      const sep = document.createElement('div');
      sep.className = 'ctx-sep';
      el.append(sep);
      return;
    }
    const row = document.createElement('div');
    row.className = 'ctx-row' + (it.off ? ' off' : '');
    row.setAttribute('role', 'menuitem');
    if (it.off) { row.setAttribute('aria-disabled', 'true'); row.title = it.off; }
    const label = document.createElement('span');
    label.className = 'ctx-label';
    label.textContent = it.label;
    const key = document.createElement('span');
    key.className = 'ctx-key';
    key.textContent = it.off || it.key || '';
    row.append(label, key);
    const i = rows.length;
    rows.push({ row, it });
    // Focus stays where it is, so an editor keeps its mode and selection.
    row.addEventListener('mousedown', (e) => e.preventDefault());
    row.addEventListener('mousemove', () => mark(i));
    row.addEventListener('click', () => pick(i));
    el.append(row);
  });
  document.body.append(el);
  place(el, xy);

  const onKey = (e) => key(e);
  const onDown = (e) => { if (!el.contains(e.target)) closeMenu(); };
  const onScroll = () => closeMenu();
  const scrollEl = opts.scrollEl;
  window.addEventListener('keydown', onKey, true);
  window.addEventListener('mousedown', onDown, true);
  window.addEventListener('blur', onScroll);
  window.addEventListener('resize', onScroll);
  if (scrollEl) scrollEl.addEventListener('scroll', onScroll);
  menu = {
    el, rows, at: -1, onPick: opts.onPick,
    close() {
      window.removeEventListener('keydown', onKey, true);
      window.removeEventListener('mousedown', onDown, true);
      window.removeEventListener('blur', onScroll);
      window.removeEventListener('resize', onScroll);
      if (scrollEl) scrollEl.removeEventListener('scroll', onScroll);
      el.remove();
    },
  };
  mark(step(-1, 1));
}

// place puts the menu at xy, turned back from the window's edges.
function place(el, xy) {
  const pad = 8;
  const w = el.offsetWidth, h = el.offsetHeight;
  let x = xy.x, y = xy.y;
  if (x + w > window.innerWidth - pad) x = Math.max(pad, window.innerWidth - w - pad);
  if (y + h > window.innerHeight - pad) y = Math.max(pad, y - h);
  el.style.left = x + 'px';
  el.style.top = y + 'px';
}

export function closeMenu() {
  if (!menu) return;
  menu.close();
  menu = null;
}

// step is the next row from i in dir that can be done, wrapping.
function step(i, dir) {
  const n = menu.rows.length;
  for (let k = 1; k <= n; k++) {
    const j = (i + dir * k + n * 2) % n;
    if (!menu.rows[j].it.off) return j;
  }
  return -1;
}

function mark(i) {
  if (!menu || i < 0 || menu.rows[i].it.off) return;
  if (menu.at >= 0) menu.rows[menu.at].row.classList.remove('cur');
  menu.at = i;
  menu.rows[i].row.classList.add('cur');
}

function pick(i) {
  if (!menu || i < 0) return;
  const { rows, onPick } = menu;
  const it = rows[i].it;
  if (it.off) return;
  closeMenu();
  if (onPick) onPick(it);
  it.run();
  redraw();
}

// key is every key while the menu is open. Ctrl+n and ctrl+p move, as in
// the palette.
function key(e) {
  const ctrlOnly = e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey;
  const k = ctrlOnly && (e.key === 'n' || e.key === 'p') ? (e.key === 'n' ? 'ArrowDown' : 'ArrowUp') : e.key;
  if ((e.metaKey || e.ctrlKey) && k === e.key) { if (e.key !== 'Meta' && e.key !== 'Control') closeMenu(); return; }
  e.preventDefault();
  e.stopPropagation();
  switch (k) {
    case 'Escape': closeMenu(); break;
    case 'ArrowDown': case 'j': mark(step(menu.at, 1)); break;
    case 'ArrowUp': case 'k': mark(step(menu.at, -1)); break;
    case 'Home': mark(step(-1, 1)); break;
    case 'End': mark(step(menu.rows.length, -1)); break;
    case 'Enter': case ' ': pick(menu.at); break;
    case 'ContextMenu': closeMenu(); break;
  }
}

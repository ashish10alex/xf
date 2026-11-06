// xf web's page: draws App and sends it every key the TUI has.

import { h, render } from './vendor/preact.mjs';
import { App, LostNotice } from './view.js';
import { runScratch, toggleResults, toggleList, flushSave, SC } from './scratch.js';
import { S, start, onRedraw, handleKey, keyName, redraw, togglePanel, toggleTerm, isTermKey, toggleTermZoom, termHasKey, isTermZoomKey, logTabKey, nextTab, toggleRight, openSymbols, isPaletteKey, isCommandsKey, isProjectsKey, isRevealKey, openScratchPalette, setPaletteQuery, isSaveKey, saveAndCompile, onMac, appWindow, newTab, hooks, leaveOverlay, lostShown, openLink } from './state.js';
import { SET, reopenAfterReload, openSettings, closeSettings } from './settings.js';
import { T, closeThemePicker } from './theme.js';
import { track } from './windows.js';
import { isSearchKey } from './search.js';
import { keyStart, keyUsed, keyEnd } from './diag.js';
import { exitKey } from './exit.js';

// An app window opens at a size the browser picks, so its first page
// fills the screen. A reload, or another page in the same window, keeps
// the size the user has given it since.
if (appWindow && newTab) {
  moveTo(screen.availLeft ?? 0, screen.availTop ?? 0);
  resizeTo(screen.availWidth, screen.availHeight);
}

const root = document.getElementById('root');
// The notice that xf web can't be reached is drawn beside the page, which
// is inert under it: no clicks, focus or typing. The keys it had come
// back to where they were when xf web does.
const lostRoot = document.body.appendChild(document.createElement('div'));
let focusBeforeLost = null;
function coverIfLost() {
  const lost = lostShown();
  if (lost === root.inert) return;
  if (lost) {
    focusBeforeLost = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null;
    root.inert = true;
    if (focusBeforeLost) focusBeforeLost.blur();
    return;
  }
  root.inert = false;
  if (focusBeforeLost && focusBeforeLost.isConnected) focusBeforeLost.focus();
  focusBeforeLost = null;
}

// A dialog that opens from another one in the same draw (a palette
// command that opens the branch switcher, say) is a new .overlay-back:
// it would fade the dimmed page out and in again, and its box rise, so
// the whole page flickers. It shows at once instead (style.css,
// data-held), as if the one dialog had changed what it shows.
const backs = () => [...root.querySelectorAll('.overlay-back')];
function holdBacks(before) {
  if (!before.length) return;
  for (const el of backs()) if (!before.includes(el)) el.dataset.held = '';
}

// After each draw this window tells the others what it shows.
// <html class="drawn"> ends the switch from the page before this one and
// shows the grain (style.css).
onRedraw(() => { const shown = backs(); render(h(App, null), root); holdBacks(shown); render(h(LostNotice, null), lostRoot); document.documentElement.classList.add('drawn'); coverIfLost(); track(); });

// In an app window a link to another site opens as openLink does, in a
// tab of the last browser window rather than a new window.
if (appWindow) {
  const outward = (e) => {
    const a = e.target instanceof Element ? e.target.closest('a[target="_blank"]') : null;
    if (!a || !/^https?:$/.test(a.protocol) || (e.type === 'auxclick' && e.button !== 1)) return;
    e.preventDefault();
    openLink(a.href);
  };
  document.addEventListener('click', outward, true);
  document.addEventListener('auxclick', outward, true);
}

// Every key is noted in the page log, with where it went (diag.js): this
// runs before every other key handler, and keyEnd after them.
window.addEventListener('keydown', keyStart, true);

// The panes' numbers (ctrl+0…3) show only while ctrl is held on its own
// for a moment, so a ctrl+key shortcut doesn't flash them. Once shown they
// stay until ctrl goes up, so ctrl+1, ctrl+2 can be pressed in a row.
let ctrlTimer = 0;
const showPaneNums = (on) => {
  clearTimeout(ctrlTimer);
  document.documentElement.classList.toggle('ctrl-held', on);
};
window.addEventListener('keydown', (e) => {
  if (e.key === 'Control' && !e.metaKey && !e.altKey && !e.shiftKey) {
    if (!e.repeat) { clearTimeout(ctrlTimer); ctrlTimer = setTimeout(() => showPaneNums(true), 300); }
  } else if (!e.ctrlKey) showPaneNums(false);
  else clearTimeout(ctrlTimer);
}, true);
window.addEventListener('keyup', (e) => { if (e.key === 'Control' || !e.ctrlKey) showPaneNums(false); }, true);
window.addEventListener('blur', () => showPaneNums(false));

// While xf web can't be reached, xf's keys do nothing: this runs before
// every other key handler and stops them. The browser's own (⌘r, ⌘w,
// ctrl+r off a Mac) still work, all but ctrl+n's blank window.
window.addEventListener('keydown', (e) => {
  if (!lostShown()) return;
  e.stopImmediatePropagation();
  const browserKey = onMac ? e.metaKey : e.ctrlKey && !(letter(e) === 'n' && !e.altKey && !e.shiftKey);
  if (!browserKey) e.preventDefault();
}, true);

// ctrl+g (ctrl on a Mac too, where cmd+g is the browser's find next)
// shows and hides Source Control (ADR 0007), from anywhere in the project
// view, the editors included: it is caught before vim, whose ctrl+g only
// says where the cursor is. Caps lock makes the key G, which is still it.
const letter = (e) => (e.key.length === 1 ? e.key.toLowerCase() : e.key);
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!(e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && letter(e) === 'g')) return;
  if (S.mode !== 'project' || S.overlay || S.checks || SET.open || SC.open || T.pick || !hooks.toggleSCM) return;
  e.preventDefault();
  e.stopPropagation();
  // Showing it gives it the keys, so an editor gives them up.
  if (!hooks.scmShown() && document.activeElement instanceof HTMLElement && document.activeElement.closest('.cm-editor')) document.activeElement.blur();
  hooks.toggleSCM();
  redraw();
}, true);

// cmd+shift+f (ctrl+shift+f off a Mac, and on a Mac either) shows Search
// with its box focused (ADR 0016), from anywhere in the project view, the
// editors included: it is caught before vim. F4 and shift+F4 go to the
// next and previous match from anywhere there too.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  const f4 = e.key === 'F4' && !e.metaKey && !e.ctrlKey && !e.altKey;
  if (!f4 && !isSearchKey(e)) return;
  if (S.mode !== 'project' || S.checks || SET.open || SC.open || T.pick || !hooks.openSearch) return;
  if ((f4 && S.overlay) || S.overlay === 'sourceConflict') return;
  e.preventDefault();
  e.stopPropagation();
  if (f4) { hooks.searchStep(e.shiftKey ? -1 : 1); redraw(); return; }
  if (S.overlay) leaveOverlay();
  hooks.openSearch();
}, true);

// cmd+shift+w (ctrl+shift+w off a Mac) shows and hides the column's
// projects tab, as ctrl+g does Source Control, from anywhere in the
// project view, the editors included; an open palette closes first.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!isProjectsKey(e)) return;
  if (S.mode !== 'project' || (S.overlay && S.overlay !== 'palette') || S.checks || SET.open || SC.open || T.pick || !hooks.toggleProjects) return;
  e.preventDefault();
  e.stopPropagation();
  if (S.overlay) leaveOverlay();
  // Showing it gives it the keys, so an editor gives them up.
  if (!hooks.projectsShown() && document.activeElement instanceof HTMLElement && document.activeElement.closest('.cm-editor')) document.activeElement.blur();
  hooks.toggleProjects();
  redraw();
}, true);

// ctrl+q (ctrl on a Mac too, where cmd+q quits the browser) leaves one
// level from anywhere, the editors included: see exit.js.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own, where vim and readline use it
  if (!(e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && letter(e) === 'q')) return;
  e.preventDefault();
  e.stopPropagation();
  exitKey();
}, true);

// ctrl+, (ctrl on a Mac too, where cmd+, is the browser's own settings)
// shows and hides the settings page from anywhere, the editors included,
// as VS Code's settings key does; the TUI keeps ",". An open overlay or the
// theme picker closes first, as its esc does. While the page asks about an
// unsaved draft only its own keys answer.
window.addEventListener('keydown', async (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!(e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && e.key === ',')) return;
  if (S.mode !== 'project' && S.mode !== 'picker') return;
  e.preventDefault();
  e.stopPropagation();
  if (T.pick) closeThemePicker();
  if (SET.open) {
    if (!SET.closing) closeSettings();
    redraw();
    return;
  }
  leaveOverlay();
  // The page takes the keys from an editor, and Scratch's editor goes while
  // it shows, so its text is saved first.
  if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
  if (SC.open) await flushSave();
  openSettings();
}, true);

// cmd+b (ctrl+b off a Mac) shows and hides the file tree, swapping
// Source Control out, as VS Code's sidebar does (ADR 0006), and in Scratch
// its list, from anywhere, the editors included: it is caught before vim,
// whose ctrl+b pages up off a Mac, so the key does one thing wherever the
// keys are, as ⌘b does on a Mac.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!(letter(e) === 'b' && !e.altKey && !e.shiftKey && (onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey))) return;
  if (T.pick) return;
  if (SC.open) {
    e.preventDefault();
    e.stopPropagation();
    toggleList();
    return;
  }
  if (S.mode !== 'project' || S.overlay || S.checks || SET.open || !hooks.toggleColumn) return;
  e.preventDefault();
  e.stopPropagation();
  // Showing the tree gives it the keys, so an editor gives them up.
  const hiding = hooks.treeShown() && !hooks.scmShown();
  if (!hiding && document.activeElement instanceof HTMLElement && document.activeElement.closest('.cm-editor')) document.activeElement.blur();
  hooks.toggleColumn();
  redraw();
}, true);

// cmd+shift+e (ctrl+shift+e off a Mac) shows the open file in the file
// tree and gives it the keys, or gives them back to the editor, as VS
// Code's Show Explorer does, from anywhere, the editors included.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!isRevealKey(e)) return;
  if (S.mode !== 'project' || S.overlay || S.checks || SET.open || SC.open || T.pick || !hooks.revealActive) return;
  e.preventDefault();
  e.stopPropagation();
  if (document.activeElement instanceof HTMLElement && document.activeElement.closest('.cm-editor')) document.activeElement.blur();
  hooks.revealActive();
  redraw();
}, true);

// ctrl+n is always xf's off a Mac, moving where something moves and
// doing nothing elsewhere. A tab never sees it (the browser's new window
// has it first), but an app window (xf web with webBrowser) does, and a
// stray press there would open a blank browser window.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!onMac && e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && letter(e) === 'n') e.preventDefault();
});

// cmd+p (ctrl+p off a Mac) finds a file from anywhere in the project
// view, as VS Code's quick open does, instead of printing the page: the
// editors included, where it is caught before vim, whose ctrl+p only moves
// up a line. Off a Mac a box keeps ctrl+p, which moves up in its list, and
// so does an editor while its completions show.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!isPaletteKey(e) || T.pick || S.mode !== 'project' || SET.open || SC.open) return;
  const t = e.target;
  const keeps = e.ctrlKey && (t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement ||
    (t instanceof Element && t.closest('.cm-editor')?.querySelector('.cm-tooltip-autocomplete')));
  if (keeps) return;
  e.preventDefault();
  if (S.overlay === 'palette') return; // its box moves on the key
  e.stopPropagation();
  handleKey('palette');
  redraw();
}, true);

// cmd+s (ctrl+s off a Mac) saves the source pane and compiles from
// anywhere in the project view, the source editor included, where it is
// caught before the editor's own save so the file is saved once. Scratch
// and the settings page keep it for their own editors; elsewhere it only
// stops the browser saving the page.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (!isSaveKey(e)) return;
  const inEditor = document.activeElement instanceof HTMLElement && document.activeElement.closest('.cm-editor');
  if ((SET.open || SC.open) && inEditor) return;
  e.preventDefault();
  if (S.mode !== 'project' || SET.open || SC.open || S.overlay === 'sourceConflict') return;
  e.stopPropagation();
  saveAndCompile().then(redraw);
}, true);

// cmd+enter (ctrl+enter off a Mac) previews the selected action's rows
// from anywhere in the project view, the source editor included, where it
// is caught before CodeMirror's own (a blank line). Other editors (the
// commit message's) and boxes keep it.
window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  if (e.key !== 'Enter' || e.altKey || e.shiftKey || !(onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey)) return;
  if (S.mode !== 'project' || SET.open || SC.open || S.overlay || S.checks || S.inspector || T.pick || !hooks.preview) return;
  const t = e.target;
  if (t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement) return;
  const cmEd = t instanceof Element && t.closest('.cm-editor');
  if (cmEd && !cmEd.closest('.src-cm')) return;
  e.preventDefault();
  e.stopPropagation();
  hooks.preview();
  redraw();
}, true);

window.addEventListener('keydown', (e) => {
  if (termHasKey(e)) return; // the terminal's own
  const t = e.target;
  // The theme picker's box handles its own keys.
  if (T.pick) return;
  // ⌥⌘b (ctrl+alt+b off a Mac) hides and shows pane 2, as VS Code's
  // secondary side bar, from anywhere in the project view, the editors
  // included. Option changes the key a Mac reports, so it is read by code.
  if (e.code === 'KeyB' && e.altKey && !e.shiftKey && (onMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey)) {
    if (S.mode === 'project' && !S.overlay && !S.checks && !SET.open && !SC.open) {
      e.preventDefault();
      toggleRight();
      redraw();
    }
    return;
  }
  // ⌥⌘→ and ⌥⌘← (ctrl+alt+→/← off a Mac) go round the log pane's tabs,
  // the terminal's included, while the log pane has the keys: from inside
  // the terminal and the jobs search too (ADR 0023).
  const logDir = logTabKey(e);
  // In the dependency inspector the same keys go round the rows' results,
  // from its filter boxes too.
  if (logDir && S.mode === 'project' && S.inspector && !S.overlay && hooks.inspectorCycle) {
    e.preventDefault();
    hooks.inspectorCycle(logDir);
    redraw();
    return;
  }
  if (logDir && S.mode === 'project' && S.showLog && S.focus === 'log' && !S.overlay && !S.checks && !SET.open && !SC.open) {
    e.preventDefault();
    nextTab(logDir);
    redraw();
    return;
  }
  // alt+z (and ⌘z on a Mac) zooms the terminal tab from inside it, where
  // z is the shell's.
  if (isTermZoomKey(e) && t instanceof Element && t.closest('.xterm') && S.mode === 'project' && !S.overlay) {
    e.preventDefault();
    toggleTermZoom();
    redraw();
    return;
  }
  // ⌘` (ctrl+`) shows the terminal tab with the keys, or hides the pane
  // when the terminal has them, from anywhere in the project view.
  if (isTermKey(e)) {
    if (S.mode === 'project' && S.caps.terminal && !S.overlay && !S.checks && !SET.open && !SC.open) {
      e.preventDefault();
      toggleTerm();
      redraw();
    }
    return;
  }
  // cmd+j / ctrl+j toggles the scratch results in Scratch view, and the
  // bottom panel in the project view (like VS Code's), on the tab it
  // showed last, even while editing in CodeMirror.
  if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && (letter(e) === 'j' || letter(e) === 'b')) {
    if (SC.open) {
      e.preventDefault();
      if (letter(e) === 'j') toggleResults(); else toggleList();
      return;
    }
    if (letter(e) === 'j' && S.mode === 'project' && !S.overlay && !S.checks && !SET.open) {
      e.preventDefault();
      togglePanel();
      redraw();
      return;
    }
  }
  // ctrl+0…3 go to the file tree, pane 1 (source), pane 2 (right side) and
  // pane 3 (the log) from anywhere in the project view, including inside
  // CodeMirror or inputs.
  if (e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && (e.key === '0' || e.key === '1' || e.key === '2' || e.key === '3')) {
    if (S.mode === 'project' && !S.overlay && !S.checks && !SET.open && !SC.open) {
      e.preventDefault();
      if ((e.key === '2' || e.key === '0' || e.key === '3') && document.activeElement instanceof HTMLElement) {
        document.activeElement.blur();
      } else if (e.key === '1' && document.activeElement instanceof HTMLElement && !document.activeElement.closest('.cm-editor')) {
        document.activeElement.blur();
      }
      handleKey('ctrl+' + e.key);
      redraw();
      return;
    }
  }
  // cmd+shift+o / ctrl+shift+o opens document symbols from anywhere in the project view
  if ((e.metaKey || e.ctrlKey) && !e.altKey && e.shiftKey && (e.key === 'o' || e.key === 'O')) {
    if (S.mode === 'project' && !S.checks && !SET.open && !SC.open) {
      e.preventDefault();
      openSymbols();
      redraw();
      return;
    }
  }
  // In Scratch, cmd+shift+p opens the palette on Scratch's own commands,
  // from the editor too; again in it, back to all of them. It waits while
  // Scratch asks something (a run to confirm, a delete, the recent results).
  if (isCommandsKey(e) && S.mode === 'project' && SC.open && !SET.open) {
    e.preventDefault();
    if (S.overlay === 'palette') setPaletteQuery('>');
    else if (!S.overlay && !SC.confirm && !SC.delAsk && !SC.recentOpen && !T.pick) {
      openScratchPalette();
    }
    redraw();
    return;
  }
  // cmd+shift+p (ctrl+shift+p off a Mac) opens the palette on its ">"
  // commands, as VS Code's command palette does.
  if (isCommandsKey(e) && S.mode === 'project' && !SET.open && !SC.open) {
    e.preventDefault();
    handleKey('commands');
    redraw();
    return;
  }
  // The Scratch editor handles its own keys, vim's included.
  if (t instanceof Element && t.closest('.cm-editor')) return;
  // cmd+enter (ctrl+enter) runs the scratch query from anywhere in the view.
  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && SC.open && !S.overlay) {
    e.preventDefault();
    runScratch();
    return;
  }
  // The browser's own shortcuts (cmd+r, cmd+c, cmd+l…) stay the browser's.
  if (e.metaKey || e.altKey) return;
  const typing = t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement;
  // An input handles its own keys; esc and the arrows it hands on are
  // handled where it is.
  if (typing && !(t.readOnly && S.overlay)) return;
  const k = keyName(e);
  if (k === 'Shift' || k === 'Control' || k === 'Meta' || k === 'Alt') return;
  const used = handleKey(k);
  keyUsed(e, used);
  if (used) {
    e.preventDefault();
    redraw();
  } else if (S.flash) {
    redraw();
  }
});
window.addEventListener('keydown', keyEnd);

start().then(reopenAfterReload);

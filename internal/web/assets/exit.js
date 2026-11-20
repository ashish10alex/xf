// ctrl+q leaves, wherever the keys are and whatever page is shown, the
// editors included (main.js catches it before vim, whose ctrl+q is a
// block-wise visual mode). Each press goes back one level: an open
// overlay or the theme picker closes, else the page (Settings, Scratch,
// Checks, the dependency inspector) goes back to the project, else the
// project closes. Nothing asks first but a project that would lose
// something (state.js askQuit); the confirm takes ctrl+q again.

import { S, redraw, hooks, leaveOverlay, askQuit, closeProject } from './state.js';
import { SET, closeSettings } from './settings.js';
import { SC, closeScratchView, closeWarehouseMenu } from './scratch.js';
import { closeChecks } from './checks.js';
import { T, closeThemePicker } from './theme.js';

// exitLevel is what ctrl+q leaves now: 'project' when it closes the
// project (asking first if something would be lost), the overlay or page
// it goes back from otherwise, or '' with nothing to leave.
export function exitLevel() {
  if (S.overlay === 'confirmQuit') return 'project';
  if (S.overlay) return 'overlay';
  if (T.pick) return 'theme';
  if (SET.open) return 'settings';
  if (S.mode === 'picker' && S.scratchPick) return 'scratchPick';
  if (S.mode !== 'project') return '';
  if (SC.open) return SC.whOpen ? 'warehouse' : SC.standalone ? 'project' : 'scratch';
  if (S.checks) return 'checks';
  if (S.inspector && hooks.closeInspector) return 'inspector';
  return 'project';
}

// exitKey leaves one level, and says whether there was one to leave.
export function exitKey() {
  switch (exitLevel()) {
    case '': return false;
    case 'overlay': leaveOverlay(); redraw(); return true;
    case 'theme': closeThemePicker(); redraw(); return true;
    case 'settings':
      if (!SET.closing) closeSettings();
      redraw();
      return true;
    case 'scratchPick': S.scratchPick = null; redraw(); return true;
    case 'warehouse': closeWarehouseMenu(); return true;
    case 'scratch': closeScratchView(); return true;
    case 'checks': closeChecks(); redraw(); return true;
    case 'inspector': hooks.closeInspector(); return true;
  }
  if (S.overlay === 'confirmQuit') closeProject();
  else if (SC.open) Promise.resolve(hooks.scratchFlush && hooks.scratchFlush()).then(() => askQuit());
  else askQuit();
  return true;
}

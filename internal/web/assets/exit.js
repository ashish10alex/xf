// ctrl+q leaves, wherever the keys are and whatever page is shown, the
// editors included (main.js catches it before vim, whose ctrl+q is a
// block-wise visual mode). Each press goes back one level: an open
// overlay or the theme picker closes, else the page (Settings, Scratch,
// Checks, the dependency inspector) goes back to the project, else the
// project closes. Nothing asks first but a project that would lose
// something (state.js askQuit); the confirm takes ctrl+q again.

import { S, redraw, hooks, leaveOverlay, askQuit, closeProject } from './state.js';
import { SET, closeSettings } from './settings.js';
import { SC, closeScratchView } from './scratch.js';
import { closeChecks } from './checks.js';
import { T, closeThemePicker } from './theme.js';

// exitKey leaves one level, and says whether there was one to leave.
export function exitKey() {
  if (S.overlay === 'confirmQuit') { closeProject(); return true; }
  if (S.overlay) { leaveOverlay(); redraw(); return true; }
  if (T.pick) { closeThemePicker(); redraw(); return true; }
  if (SET.open) {
    if (!SET.closing) closeSettings();
    redraw();
    return true;
  }
  if (S.mode !== 'project') return false;
  if (SC.open) {
    if (SC.standalone) return false; // there is no project page to go back to
    closeScratchView();
    return true;
  }
  if (S.checks) { closeChecks(); redraw(); return true; }
  if (S.inspector && hooks.closeInspector) { hooks.closeInspector(); return true; }
  askQuit();
  return true;
}

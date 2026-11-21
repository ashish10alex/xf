// The SQL warehouse and (for standalone Databricks scratch) ~/.databrickscfg
// profile pickers (ADR 0042). Loaded lazily on first use.

import { S, redraw, hooks, get, post, say, checkModel } from './state.js';

// W is the SQL warehouse picker's state while it is open; then runs once a
// pick is saved.
export const W = { loading: false, list: [], cursor: 0, current: '', saveNote: '', error: '', saving: false, then: null };

// openWarehousePicker lists the SQL warehouses; then, if given, runs once
// one is chosen (a scratch query asking again), else the model is checked.
export async function openWarehousePicker(then) {
  await import('./databricks-view.js');
  Object.assign(W, { loading: true, list: [], cursor: 0, current: '', saveNote: '', error: '', saving: false, then: then || null });
  S.overlay = 'dbxWarehouse';
  redraw();
  try {
    const r = await get('/api/databricks/warehouses');
    W.list = r.warehouses || [];
    W.current = r.current || '';
    W.saveNote = r.saveNote || '';
    W.error = r.error || (W.list.length ? '' : 'the workspace has no SQL warehouse you can use');
    const curIdx = W.current ? W.list.findIndex((w) => w.id === W.current) : -1;
    const up = W.list.findIndex((w) => w.state === 'RUNNING' || w.state === 'STARTED');
    W.cursor = curIdx >= 0 ? curIdx : Math.max(up, 0);
  } catch (e) {
    W.error = String(e.message || e);
  }
  W.loading = false;
  redraw();
}

export function closePicker() {
  if (S.overlay === 'dbxWarehouse') S.overlay = null;
}

// choose saves the warehouse under the cursor, then does what the picker
// was opened for: by default, checks the model again.
export async function choose(i) {
  const w = W.list[i ?? W.cursor];
  if (!w || W.saving) return;
  W.saving = true;
  redraw();
  const r = await post('/api/databricks/warehouse', { id: w.id });
  W.saving = false;
  if (!r.ok) { W.error = r.message || 'not saved'; redraw(); return; }
  W.current = w.id;
  if (hooks.dbxUpdated) hooks.dbxUpdated({ warehouse: w.id });
  closePicker();
  say(r.message);
  if (W.then) W.then();
  else if (S.view) checkModel();
  redraw();
}

export function pickerKey(k) {
  switch (k) {
    case 'esc': closePicker(); say('no SQL warehouse chosen: queries need one'); return true;
    case 'j': case 'down': case 'ctrl+n': W.cursor = Math.min(W.cursor + 1, Math.max(W.list.length - 1, 0)); return true;
    case 'k': case 'up': case 'ctrl+p': W.cursor = Math.max(W.cursor - 1, 0); return true;
    case 'enter': choose(); return true;
  }
  return true;
}

hooks.dbxKey = pickerKey;

// ---- standalone Databricks CLI profile picker ----

export const P = { loading: false, list: [], cursor: 0, current: '', host: '', authHint: '', error: '', saving: false, then: null };

export async function openProfilePicker(then) {
  await import('./databricks-view.js');
  Object.assign(P, { loading: true, list: [], cursor: 0, current: '', host: '', authHint: '', error: '', saving: false, then: then || null });
  S.overlay = 'dbxProfile';
  redraw();
  try {
    const r = await get('/api/databricks/profiles');
    P.list = r.profiles || [];
    P.current = r.current || '';
    P.host = r.host || '';
    P.authHint = r.authHint || '';
    const idx = P.current ? P.list.findIndex((p) => p.name === P.current) : 0;
    P.cursor = Math.max(idx, 0);
  } catch (e) {
    P.error = String(e.message || e);
  }
  P.loading = false;
  redraw();
}

export function closeProfilePicker() {
  if (S.overlay === 'dbxProfile') S.overlay = null;
}

export async function chooseProfile(i) {
  const p = P.list[i ?? P.cursor];
  if (!p || P.saving) return;
  if (hooks.scratchRunning && hooks.scratchRunning()) {
    P.error = 'cancel the running query before switching Databricks profile';
    redraw();
    return;
  }
  P.saving = true;
  P.error = '';
  redraw();
  try {
    const r = await post('/api/databricks/profile', { name: p.name });
    P.saving = false;
    if (!r.ok) { P.error = r.message || 'could not switch profile'; redraw(); return; }
    P.current = r.current || p.name;
    P.host = r.host || p.host;
    if (hooks.dbxUpdated) hooks.dbxUpdated({ profile: P.current, host: P.host, warehouse: r.warehouse || '', needsProfile: false });
    const next = P.then;
    closeProfilePicker();
    say(r.message);
    if (!r.chosen) {
      openWarehousePicker(next || (() => {}));
    } else if (next) {
      next();
    }
  } catch (e) {
    P.saving = false;
    P.error = String(e.message || e);
  }
  redraw();
}

export function profilePickerKey(k) {
  switch (k) {
    case 'esc': closeProfilePicker(); return true;
    case 'j': case 'down': case 'ctrl+n': P.cursor = Math.min(P.cursor + 1, Math.max(P.list.length - 1, 0)); return true;
    case 'k': case 'up': case 'ctrl+p': P.cursor = Math.max(P.cursor - 1, 0); return true;
    case 'enter': chooseProfile(); return true;
  }
  return true;
}

hooks.dbxProfileKey = profilePickerKey;

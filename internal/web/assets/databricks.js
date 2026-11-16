// The SQL warehouse a Databricks project's checks run on (ADR 0042), when
// neither the databricksWarehouse setting nor the dbt target's http_path
// names one: a list of the workspace's warehouses to pick from, saved to
// the project's config file. Loaded the first time it is needed
// (state.js's openWarehousePicker).

import { S, redraw, hooks, get, post, say, checkModel } from './state.js';

// W is the picker's state while it is open.
export const W = { loading: false, list: [], cursor: 0, error: '', saving: false };

export async function openWarehousePicker() {
  await import('./databricks-view.js');
  Object.assign(W, { loading: true, list: [], cursor: 0, error: '', saving: false });
  S.overlay = 'dbxWarehouse';
  redraw();
  try {
    const r = await get('/api/databricks/warehouses');
    W.list = r.warehouses || [];
    W.error = r.error || (W.list.length ? '' : 'the workspace has no SQL warehouse you can use');
    // A running one first: it checks without starting.
    const up = W.list.findIndex((w) => w.state === 'RUNNING');
    W.cursor = Math.max(up, 0);
  } catch (e) {
    W.error = String(e.message || e);
  }
  W.loading = false;
  redraw();
}

export function closePicker() {
  if (S.overlay === 'dbxWarehouse') S.overlay = null;
}

// choose saves the warehouse under the cursor and checks the model again.
export async function choose(i) {
  const w = W.list[i ?? W.cursor];
  if (!w || W.saving) return;
  W.saving = true;
  redraw();
  const r = await post('/api/databricks/warehouse', { id: w.id });
  W.saving = false;
  if (!r.ok) { W.error = r.message || 'not saved'; redraw(); return; }
  closePicker();
  say(r.message);
  if (S.view) checkModel();
}

export function pickerKey(k) {
  switch (k) {
    case 'esc': closePicker(); say('no SQL warehouse chosen: checks need one'); return true;
    case 'j': case 'down': case 'ctrl+n': W.cursor = Math.min(W.cursor + 1, Math.max(W.list.length - 1, 0)); return true;
    case 'k': case 'up': case 'ctrl+p': W.cursor = Math.max(W.cursor - 1, 0); return true;
    case 'enter': choose(); return true;
  }
  return true;
}

hooks.dbxKey = pickerKey;

// Pane 2's target section: what a project's compiles add to the names it
// builds. A Dataform project's table prefix, schema suffix and database
// suffix, a dbt project's target. They are the settings of the same
// names, read and written as the settings page does, so the page, the
// section and .xf/config.json always agree. The engine compiles with what
// the project opened with, so a change reopens the project (ADR 0022).

import { S, get, hooks, post, redraw, say } from './state.js';

// dataformKeys are the Dataform settings the section edits, in its order.
export const dataformKeys = [
  { key: 'tablePrefix', label: 'prefix', title: '--table-prefix · namePrefix in workflow_settings.yaml' },
  { key: 'schemaSuffix', label: 'schema', title: '--schema-suffix · datasetSuffix in workflow_settings.yaml' },
  { key: 'databaseSuffix', label: 'database', title: '--database-suffix · projectSuffix in workflow_settings.yaml' },
];
const dbtKey = 'dbtTarget';

export const TGT = {
  // keys are the settings page's keys by name; pending those changed in a
  // file since the project opened, which a reopen applies.
  keys: {},
  pending: [],
  // view is /api/target: where the compile builds, and dbt's profile.
  view: null,
  // drafts are the fields' text while it differs from the file's.
  drafts: {},
  // errors are a field's refused value's message, by key.
  errors: {},
  saving: false,
  open: true,
};

const isDbt = () => S.boot && S.boot.backend === 'dbt';
const storeKey = () => 'xf-web:target-open:' + (S.boot ? S.boot.root : '');

// shown is whether the section shows at all: in a project with a compile,
// for an action, not a declarations file.
export const shown = () => !!(S.mode === 'project' && S.boot && S.state && S.state.hasGraph);

let seq = 0;
// load reads the settings and where the compile builds.
export async function load() {
  if (S.mode !== 'project') return;
  const n = ++seq;
  try {
    const [a, v] = await Promise.all([get('/api/settings'), get('/api/target')]);
    if (n !== seq) return;
    TGT.keys = Object.fromEntries((a.keys || []).map((k) => [k.key, k]));
    TGT.pending = a.pending || [];
    TGT.view = v;
  } catch { /* the section shows nothing more until the next compile */ }
  redraw();
}

// level is the file the section writes: the one that sets any of its
// keys, the project's when neither does.
export function level() {
  const ks = isDbt() ? [dbtKey] : dataformKeys.map((d) => d.key);
  if (ks.some((k) => TGT.keys[k] && TGT.keys[k].project != null)) return 'project';
  if (ks.some((k) => TGT.keys[k] && TGT.keys[k].user != null)) return 'user';
  return 'project';
}

// fileValue is what the section's file says of key, "" for nothing.
export function fileValue(key) {
  const k = TGT.keys[key];
  if (!k) return '';
  const v = level() === 'project' ? k.project : k.user;
  return v == null ? '' : v;
}

// flagged is the command-line flag that sets key for this run, which no
// file overrides; null for none.
export const flagged = (key) => (TGT.keys[key] && TGT.keys[key].flag != null ? TGT.keys[key].flag : null);

export const fieldText = (key) => (key in TGT.drafts ? TGT.drafts[key] : fileValue(key));

export function setDraft(key, text) {
  TGT.drafts[key] = text;
  delete TGT.errors[key];
  redraw();
}

// undo puts a field back to the file's value (esc).
export function undo(key) {
  delete TGT.drafts[key];
  delete TGT.errors[key];
  redraw();
}

// commit saves a field's draft, if it changed, and reopens the project so
// its compile uses it. An emptied field takes the key out of the file.
export async function commit(key) {
  if (!(key in TGT.drafts)) return;
  const v = TGT.drafts[key].trim();
  if (v === fileValue(key)) { undo(key); return; }
  const ok = await write(level(), key, v);
  if (ok) await reopen();
}

// write sets key to v in lv's file, or with v "" takes it out.
async function write(lv, key, v) {
  TGT.saving = true;
  redraw();
  try {
    await post('/api/settings', v === '' ? { level: lv, key, unset: true } : { level: lv, key, value: v });
    delete TGT.drafts[key];
    delete TGT.errors[key];
    return true;
  } catch (e) {
    TGT.errors[key] = e.message;
    say(key + ': ' + e.message);
    return false;
  } finally {
    TGT.saving = false;
    redraw();
  }
}

// pickTarget sets a dbt project's target; the profile's default takes the
// key out, so the profile decides again.
export async function pickTarget(name) {
  const p = TGT.view && TGT.view.profile;
  const now = (TGT.view && TGT.view.target) || (p && p.default) || '';
  if (name === now || flagged(dbtKey) != null) return;
  const v = p && name === p.default ? '' : name;
  if (await write(level(), dbtKey, v)) await reopen();
}

// reopen opens the project again, so the engine compiles with what was
// saved; every tab on it reloads, on the same action (reopen.go).
export async function reopen() {
  TGT.saving = true;
  redraw();
  try {
    await post('/api/reopen');
  } catch (e) {
    // The reload cuts the request short: the page is going, not failing.
    if (S.switching) return;
    TGT.saving = false;
    say('saved; ' + e.message);
    load();
  }
}

// reopening is whether the project is opening again, from this page or
// another: the old compile stays on show until the new one is in.
export const reopening = () => TGT.saving || !!S.reopening;

export function toggleOpen() {
  TGT.open = !TGT.open;
  try { localStorage.setItem(storeKey(), TGT.open ? '1' : '0'); } catch { /* this page only */ }
  redraw();
}

// summary is the folded section's line: what is set, or that nothing is.
export function summary() {
  if (isDbt()) {
    const v = TGT.view || {};
    const p = v.profile;
    return v.target || (p && p.default) || 'the profile\'s default';
  }
  const parts = [];
  for (const d of dataformKeys) {
    const k = TGT.keys[d.key];
    const val = k ? k.value : '';
    if (val && val !== '(none)') parts.push(d.label + (d.key === 'tablePrefix' ? ' ' + val + '_' : ' _' + val));
  }
  return parts.length ? parts.join(' · ') : 'as workflow_settings.yaml';
}

hooks.targetCompiled = () => {
  try { TGT.open = localStorage.getItem(storeKey()) !== '0'; } catch { /* open */ }
  load();
};
hooks.targetSettings = () => load();

// prodCompilerOptions as a form (ADR 0037): the prod project and dataset
// in boxes of their own, every other flag kept as written. The form reads
// and writes the one string the config holds, so the CLI flag and the
// files stay as they are.

// The flags with a box of their own, by the box.
const boxFlags = { project: '--default-database', dataset: '--default-schema' };
// The flags that change the names a compile builds besides those two:
// with one in more flags, xf can't tell whether prod names dev's tables.
const nameFlags = ['--table-prefix', '--schema-suffix', '--database-suffix'];

// tokens are s's arguments as dataform.SplitOptions splits them (on
// spaces outside quotes), each with its text as written (raw) and
// without its quotes (arg); null when a quote isn't closed.
function tokens(s) {
  const out = [];
  let cur = null, quote = '';
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (quote) {
      if (c === quote) quote = ''; else cur.arg += c;
      cur.raw += c;
      continue;
    }
    if (c === ' ' || c === '\t' || c === '\n') {
      if (cur) { out.push(cur); cur = null; }
      continue;
    }
    if (!cur) cur = { raw: '', arg: '' };
    cur.raw += c;
    if (c === '"' || c === "'") quote = c; else cur.arg += c;
  }
  if (quote) return null;
  if (cur) out.push(cur);
  return out;
}

// splitOptions reads prodCompilerOptions into the form's boxes:
// --default-database and --default-schema, as `=x` or ` x`, go to project
// and dataset; the other arguments stay in more, as written. A string
// with a quote not closed goes whole into more.
export function splitOptions(s) {
  const out = { project: '', dataset: '', more: '' };
  const ts = tokens(s || '');
  if (!ts) { out.more = s.trim(); return out; }
  const more = [];
  for (let i = 0; i < ts.length; i++) {
    const t = ts[i];
    const box = Object.keys(boxFlags).find((b) => t.arg === boxFlags[b] || t.arg.startsWith(boxFlags[b] + '='));
    if (!box) { more.push(t.raw); continue; }
    if (t.arg === boxFlags[box]) {
      const next = ts[i + 1];
      if (!next || next.arg.startsWith('--')) { more.push(t.raw); continue; }
      out[box] = next.arg;
      i++;
    } else out[box] = t.arg.slice(boxFlags[box].length + 1);
  }
  out.more = more.join(' ');
  return out;
}

// joinOptions writes the form's boxes back as prodCompilerOptions, the
// boxes as `--flag=value` before more; "" when every box is empty.
export function joinOptions(f) {
  const parts = [];
  for (const box of Object.keys(boxFlags)) {
    const v = (f[box] || '').trim();
    if (v) parts.push(boxFlags[box] + '=' + (/[\s'"]/.test(v) ? JSON.stringify(v) : v));
  }
  const more = (f.more || '').trim();
  if (more) parts.push(more);
  return parts.join(' ');
}

// sameAsDev says the form's prod names the tables dev builds: the same
// project, dataset, prefix and suffixes. names are /api/target's: the
// workflow settings' (what an empty box gives) and the dev compile's. A
// name flag in more flags, or a dev compile not known, says nothing.
export function sameAsDev(f, names) {
  if (!names || !names.dev || !names.workflow) return false;
  const more = tokens(f.more || '');
  if (!more || more.some((t) => nameFlags.some((n) => t.arg === n || t.arg.startsWith(n + '=')))) return false;
  const w = names.workflow, d = names.dev;
  const prod = {
    project: (f.project || '').trim() || w.project,
    dataset: (f.dataset || '').trim() || w.dataset,
    prefix: w.prefix, schemaSuffix: w.schemaSuffix, databaseSuffix: w.databaseSuffix,
  };
  return ['project', 'dataset', 'prefix', 'schemaSuffix', 'databaseSuffix'].every((k) => (prod[k] || '') === (d[k] || ''));
}

// boxHint is an empty box's placeholder: what the prod compile then uses,
// said as dev's when dev uses it too.
export function boxHint(box, names) {
  const key = box === 'project' ? 'project' : 'dataset';
  const w = names && names.workflow && names.workflow[key];
  if (!w) return box === 'project' ? 'e.g. acme-prod' : 'e.g. analytics';
  const d = names.dev && names.dev[key];
  return (d === undefined || d === w ? 'same as dev: ' : 'workflow_settings.yaml: ') + w;
}

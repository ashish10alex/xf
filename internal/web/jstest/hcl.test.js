// Terraform's .tf files are coloured as HCL in the source pane.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cm from '../assets/vendor/codemirror.mjs';
import { languageFor } from '../assets/sqlx.js';

// tokens is each coloured piece of text with its token's name.
function tokens(text) {
  const state = cm.EditorState.create({ doc: text, extensions: languageFor('hcl', false) });
  const out = [];
  cm.syntaxTree(state).iterate({
    enter(n) {
      if (n.type.isTop) return;
      out.push([state.doc.sliceString(n.from, n.to), n.name]);
    },
  });
  return out;
}

test('a .tf file is coloured', () => {
  const got = tokens(`# a dataset
resource "google_bigquery_dataset" "raw" {
  dataset_id = "raw_\${var.env}"
  labels     = { team = local.team }
  count      = length(var.regions) > 0 ? 1 : 0
  enabled    = true
  /* note */
  description = <<-EOT
    Raw \${upper(var.env)} data
  EOT
}
`);
  const nameOf = (text, nth = 0) => got.filter(([t]) => t === text)[nth]?.[1];
  assert.equal(nameOf('# a dataset'), 'comment');
  assert.equal(nameOf('resource'), 'keyword');
  assert.equal(nameOf('"google_bigquery_dataset"'), 'typeName');
  assert.equal(nameOf('"raw"'), 'typeName');
  assert.equal(nameOf('dataset_id'), 'propertyName');
  assert.equal(nameOf('"raw_'), 'string');
  assert.equal(nameOf('${'), 'punctuation');
  assert.equal(nameOf('var'), 'variableName.special');
  assert.equal(nameOf('env'), 'propertyName');
  assert.equal(nameOf('"'), 'string', 'the string goes on past its ${ }');
  assert.equal(nameOf('length'), 'variableName.function');
  assert.equal(nameOf('0'), 'number');
  assert.equal(nameOf('true'), 'bool');
  assert.equal(nameOf('/* note */'), 'comment');
  assert.equal(nameOf('    Raw '), 'string');
  assert.equal(nameOf('upper'), 'variableName.function', "a heredoc's ${ } is coloured");
  assert.equal(nameOf(' data'), 'string');
  assert.equal(nameOf('  EOT'), 'string');
  assert.equal(got[got.length - 1][1], 'punctuation', 'the heredoc ends at EOT');
});

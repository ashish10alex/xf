// Every module of the page loads: its imports name exports that exist,
// and nothing it runs on load throws. node --check only parses a file;
// this links them, as the browser does.

import { test } from 'node:test';
import { readdirSync } from 'node:fs';

const assets = new URL('../assets/', import.meta.url);
// main.js draws the page into #root, which only a browser has.
const modules = readdirSync(assets).filter((f) => f.endsWith('.js') && f !== 'main.js');

for (const f of modules) {
  test(`${f} loads`, async () => {
    await import(new URL(f, assets));
  });
}

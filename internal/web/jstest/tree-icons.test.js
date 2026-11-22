// Generic file-type icons for the file explorer: O(1) lookup by filename
// or extension, case-insensitive, prototype-safe, and backed by a matching
// CSS ::before mask rule in style.css.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileIcon } from '../assets/tree.js';

const css = readFileSync(new URL('../assets/style.css', import.meta.url), 'utf8');

test('folders pick dir or dir-open regardless of name', () => {
  assert.equal(fileIcon('definitions', true, false), 'dir');
  assert.equal(fileIcon('definitions', true, true), 'dir-open');
  assert.equal(fileIcon('models.sql', true, false), 'dir');
  assert.equal(fileIcon('.git', true, true), 'dir-open');
});

test('common file extensions map to generic icon categories', () => {
  const cases = [
    ['orders.sqlx', 'sql'],
    ['orders.sql', 'sql'],
    ['ORDERS.SQLX', 'sql'],
    ['helpers.js', 'js'],
    ['config.mjs', 'js'],
    ['app.tsx', 'ts'],
    ['train.py', 'py'],
    ['explore.ipynb', 'py'],
    ['main.go', 'code'],
    ['lib.rs', 'code'],
    ['workflow_settings.yaml', 'yaml'],
    ['dbt_project.yml', 'yaml'],
    ['package.json', 'json'],
    ['events.jsonl', 'json'],
    ['pyproject.toml', 'config'],
    ['app.ini', 'config'],
    ['main.tf', 'hcl'],
    ['terragrunt.hcl', 'hcl'],
    ['README.md', 'md'],
    ['guide.mdx', 'md'],
    ['dbt.log', 'txt'],
    ['notes.txt', 'txt'],
    ['country_codes.csv', 'csv'],
    ['events.parquet', 'csv'],
    ['load.sh', 'sh'],
    ['install.ps1', 'sh'],
    ['index.html', 'web'],
    ['template.j2', 'web'],
    ['style.css', 'css'],
    ['theme.scss', 'css'],
    ['icon.svg', 'img'],
    ['hero.webp', 'img'],
    ['cert.pem', 'lock'],
    ['release.tar.gz', 'archive'],
    ['bundle.zip', 'archive'],
    ['unknown.xyz', 'file'],
    ['noext', 'file'],
    ['', 'file'],
  ];
  for (const [name, want] of cases) {
    assert.equal(fileIcon(name, false, false), want, name);
  }
});

test('special filenames and prefixes take precedence over extension', () => {
  const cases = [
    ['package-lock.json', 'lock'],
    ['pnpm-lock.yaml', 'lock'],
    ['yarn.lock', 'lock'],
    ['go.sum', 'lock'],
    ['go.mod', 'config'],
    ['Dockerfile', 'config'],
    ['Dockerfile.prod', 'config'],
    ['Makefile', 'config'],
    ['justfile', 'config'],
    ['.env', 'config'],
    ['.env.local', 'config'],
    ['.sqlfluff', 'config'],
    ['.gitignore', 'git'],
    ['.gitattributes', 'git'],
    ['.bashrc', 'sh'],
    ['.zshrc', 'sh'],
    ['README', 'md'],
    ['LICENSE', 'txt'],
    ['CODEOWNERS', 'txt'],
  ];
  for (const [name, want] of cases) {
    assert.equal(fileIcon(name, false, false), want, name);
  }
});

test('Object.prototype keys never match as file names or extensions', () => {
  for (const name of ['constructor', 'toString', 'valueOf', '__proto__', 'a.constructor', 'a.toString', 'a.hasOwnProperty']) {
    assert.equal(fileIcon(name, false, false), 'file', name);
  }
});

test('every icon key used by fileIcon has a matching .fi-<key>::before rule in style.css', () => {
  const keys = new Set([
    fileIcon('d', true, false),
    fileIcon('d', true, true),
    fileIcon('x', false, false),
    fileIcon('a.sqlx', false, false),
    fileIcon('a.js', false, false),
    fileIcon('a.ts', false, false),
    fileIcon('a.py', false, false),
    fileIcon('a.go', false, false),
    fileIcon('a.json', false, false),
    fileIcon('a.yaml', false, false),
    fileIcon('a.toml', false, false),
    fileIcon('a.tf', false, false),
    fileIcon('a.md', false, false),
    fileIcon('a.txt', false, false),
    fileIcon('a.csv', false, false),
    fileIcon('a.sh', false, false),
    fileIcon('a.html', false, false),
    fileIcon('a.css', false, false),
    fileIcon('a.png', false, false),
    fileIcon('.gitignore', false, false),
    fileIcon('yarn.lock', false, false),
    fileIcon('a.zip', false, false),
  ]);
  for (const k of keys) {
    assert.match(css, new RegExp(`\\.fi-${k}::before\\b`), `missing CSS rule for .fi-${k}::before`);
  }
});

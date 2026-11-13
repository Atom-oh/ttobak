import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

// The Mac app has no CI of its own. `release.sh` refuses a release whose
// version fields disagree; this catches the disagreement at review time.
const read = path => readFileSync(new URL(`../../mac-app/${path}`, import.meta.url), 'utf8');

test('Mac app version fields agree', () => {
  const tauri = JSON.parse(read('src-tauri/tauri.conf.json')).version;
  const npm = JSON.parse(read('package.json')).version;
  const cargo = /^\[package\][^[]*?^version = "([^"]+)"/ms.exec(read('src-tauri/Cargo.toml'))?.[1];
  assert.match(tauri, /^\d+\.\d+\.\d+$/);
  assert.equal(cargo, tauri);
  assert.equal(npm, tauri);
});

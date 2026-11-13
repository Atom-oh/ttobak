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

// The capture aggregate must not use tap auto-start: with it, Core Audio holds
// the whole aggregate (microphone included) until some tapped process plays
// sound, so a quiet Mac captured nothing (observed 2026-11-13, ADR-024).
test('Mac capture does not wait for system sound to start', () => {
  const audio = read('src-tauri/src/audio.rs');
  const setting = /kAudioAggregateDeviceTapAutoStartKey,\s*CFBoolean::(true|false)_value\(\)/.exec(audio);
  assert.ok(setting, 'the aggregate sets kAudioAggregateDeviceTapAutoStartKey');
  assert.equal(setting[1], 'false');
});

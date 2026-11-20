import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

const SRC = new URL('../src/', import.meta.url).pathname;

function load() {
  const source = readFileSync(join(SRC, 'lib/slideDetect.ts'), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Uint8Array, Float32Array, Math, Number, Error });
  return exports;
}

const lib = load();
const { GRID_W, GRID_H, GRID_CELLS, SlideChangeDetector, signatureFromRgba } = lib;

/** A "slide": a light background with dark text bars placed by seed. */
function slide(seed) {
  const sig = new Uint8Array(GRID_CELLS).fill(240);
  for (let row = 4 + (seed % 3); row < GRID_H - 4; row += 4) {
    const len = 10 + ((seed * 7 + row * 13) % 40);
    for (let x = 4; x < 4 + len; x++) sig[row * GRID_W + x] = 30;
  }
  return sig;
}

/** Overlays a flickering "participant video" tile in the top-right corner. */
function withVideo(sig, tick) {
  const out = Uint8Array.from(sig);
  for (let y = 0; y < 8; y++) {
    for (let x = GRID_W - 12; x < GRID_W; x++) out[y * GRID_W + x] = (tick * 97 + x * 31 + y * 17) % 256;
  }
  return out;
}

function feed(detector, frames, start = 0, stepMs = 1000) {
  return frames.map((sig, i) => detector.push(sig, start + i * stepMs));
}

const repeat = (sig, n) => Array.from({ length: n }, () => sig);

test('captures a slide once it is stable, and the next slide when it changes', () => {
  const d = new SlideChangeDetector();
  const a = slide(1);
  const b = slide(2);
  const out = feed(d, [...repeat(a, 5), ...repeat(b, 5)]);
  assert.equal(out.filter((x) => x === 'new-slide').length, 2);
  assert.equal(out.indexOf('new-slide'), 2); // first sample + two still samples
  assert.equal(d.captureCount, 2);
});

test('a flickering video tile does not trigger captures or block a static slide', () => {
  const d = new SlideChangeDetector();
  const a = slide(3);
  const frames = Array.from({ length: 12 }, (_, i) => withVideo(a, i));
  const out = feed(d, frames);
  assert.equal(out.filter((x) => x === 'new-slide').length, 1);
});

test('returning to an earlier slide is not uploaded again', () => {
  const d = new SlideChangeDetector();
  const a = slide(4);
  const b = slide(5);
  const out = feed(d, [...repeat(a, 5), ...repeat(b, 5), ...repeat(a, 5)]);
  assert.equal(out.filter((x) => x === 'new-slide').length, 2);
});

test('a blank screen is never captured', () => {
  const d = new SlideChangeDetector();
  const out = feed(d, repeat(new Uint8Array(GRID_CELLS).fill(0), 6));
  assert.ok(!out.includes('new-slide'));
});

test('minimum gap delays a fast second slide until the gap has passed', () => {
  const d = new SlideChangeDetector({ minGapMs: 10_000 });
  const out = feed(d, [...repeat(slide(6), 3), ...repeat(slide(7), 12)]);
  const captures = out.map((x, i) => (x === 'new-slide' ? i : -1)).filter((i) => i >= 0);
  assert.equal(captures.length, 2);
  assert.ok(captures[1] - captures[0] >= 10);
});

test('stops with limit once the cap is reached', () => {
  const d = new SlideChangeDetector({ maxCaptures: 1, minGapMs: 0 });
  const out = feed(d, [...repeat(slide(8), 4), ...repeat(slide(9), 4)]);
  // The session stops at the first 'limit'.
  assert.deepEqual(out.filter((x) => x !== 'skip').slice(0, 2), ['new-slide', 'limit']);
});

test('the cap is reported on the very next sample, without waiting for a new slide', () => {
  const d = new SlideChangeDetector({ maxCaptures: 1 });
  const out = feed(d, repeat(slide(10), 4));
  assert.deepEqual(out, ['skip', 'skip', 'new-slide', 'limit']);
});

test('signatureFromRgba averages pixels into grid cells', () => {
  const w = GRID_W * 2;
  const h = GRID_H * 2;
  const data = new Uint8Array(w * h * 4);
  for (let i = 0; i < w * h; i++) {
    const x = i % w;
    const v = x < w / 2 ? 0 : 255;
    data.set([v, v, v, 255], i * 4);
  }
  const sig = signatureFromRgba(data, w, h);
  assert.equal(sig.length, GRID_CELLS);
  assert.equal(sig[0], 0);
  assert.equal(sig[GRID_W - 1], 255);
});

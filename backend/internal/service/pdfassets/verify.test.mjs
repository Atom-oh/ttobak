import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  fontPath, licensePath, verifyPDFAssets,
} from './verify.mjs';

test('restored official font and license match their pinned digests', () => {
  assert.doesNotThrow(() => verifyPDFAssets());
});

test('missing or corrupt assets stop the build verification', t => {
  const directory = mkdtempSync(join(tmpdir(), 'ttobak-pdf-assets-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const font = join(directory, 'font.ttf');
  const license = join(directory, 'LICENSE.txt');
  assert.throws(() => verifyPDFAssets(font, license), /dependency is missing/);
  writeFileSync(font, readFileSync(fontPath));
  writeFileSync(license, readFileSync(licensePath));
  assert.doesNotThrow(() => verifyPDFAssets(font, license));
  writeFileSync(font, Buffer.from('corrupt font'));
  assert.throws(() => verifyPDFAssets(font, license), /integrity verification/);
  writeFileSync(font, readFileSync(fontPath));
  writeFileSync(license, Buffer.from('corrupt license'));
  assert.throws(() => verifyPDFAssets(font, license), /integrity verification/);
});

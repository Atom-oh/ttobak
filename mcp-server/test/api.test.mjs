// Tests for the pure guard/inference helpers in src/api.ts.
// Run with: npm test (builds first -- imports from dist/).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, writeFileSync, mkdirSync, symlinkSync, rmSync } from 'node:fs';
import { tmpdir, homedir } from 'node:os';
import { join } from 'node:path';
import { guardUploadPath, resolveFileMeta, MAX_UPLOAD_BYTES, MAX_KB_UPLOAD_BYTES } from '../dist/api.js';

test('resolveFileMeta: extensionless name with explicit fileType survives intact', () => {
  const { name, type } = resolveFileMeta('/x/notes', undefined, 'text/markdown');
  assert.equal(name, 'notes'); // regression: slice(0, -0) used to empty it
  assert.equal(type, 'text/markdown');
});

test('resolveFileMeta: uppercase extension is lowercased for the upload key', () => {
  const { name, type } = resolveFileMeta('/x/DECK.PPTX');
  assert.equal(name, 'DECK.pptx');
  assert.match(type, /presentationml/);
});

test('resolveFileMeta: lowercase name passes through unchanged', () => {
  assert.equal(resolveFileMeta('/x/deck.pdf').name, 'deck.pdf');
});

test('resolveFileMeta: unknown extension without fileType throws', () => {
  assert.throws(() => resolveFileMeta('/x/archive.zip'), /pass fileType explicitly/);
});

test('resolveFileMeta: fileName override drives MIME inference, not the local path', () => {
  const { name, type } = resolveFileMeta('/x/deck.pptx', 'quarterly.pdf');
  assert.equal(name, 'quarterly.pdf');
  assert.equal(type, 'application/pdf');
});

test('guardUploadPath: rejects relative paths', () => {
  assert.throws(() => guardUploadPath('relative/doc.pdf', MAX_UPLOAD_BYTES), /absolute path/);
});

test('guardUploadPath: wraps missing files in a clear error', () => {
  assert.throws(() => guardUploadPath('/definitely/not/a/real/file.pdf', MAX_UPLOAD_BYTES), /File not found/);
});

test('guardUploadPath: rejects non-regular files (directory)', () => {
  const dir = mkTmpDir();
  try {
    assert.throws(() => guardUploadPath(dir, MAX_UPLOAD_BYTES), /not a regular file/);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('guardUploadPath: enforces the size cap', () => {
  const dir = mkTmpDir();
  const f = join(dir, 'big.pdf');
  writeFileSync(f, 'x'.repeat(16));
  try {
    assert.throws(() => guardUploadPath(f, 8), /exceeds the 8-byte limit/);
    assert.equal(guardUploadPath(f, MAX_KB_UPLOAD_BYTES).size, 16);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('guardUploadPath: blocks dotdirs under $HOME, including via symlink', () => {
  // Fixture inside the real $HOME -- guardUploadPath reads homedir() itself.
  const hidden = mkdtempSync(join(homedir(), '.guard-test-'));
  const secret = join(hidden, 'creds.pdf');
  writeFileSync(secret, 'x');
  const outside = mkTmpDir();
  const link = join(outside, 'looks-legit.pdf');
  symlinkSync(secret, link);
  try {
    assert.throws(() => guardUploadPath(secret, MAX_UPLOAD_BYTES), /hidden\/credentials directory/);
    // realpathSync must follow the symlink back into the hidden dir
    assert.throws(() => guardUploadPath(link, MAX_UPLOAD_BYTES), /hidden\/credentials directory/);
  } finally {
    rmSync(hidden, { recursive: true, force: true });
    rmSync(outside, { recursive: true, force: true });
  }
});

test('guardUploadPath: blocks system paths', () => {
  // /etc/hosts exists on every Linux/macOS box this server runs on
  assert.throws(() => guardUploadPath('/etc/hosts', MAX_UPLOAD_BYTES), /system directory/);
});

test('guardUploadPath: blocks secret-shaped filenames', () => {
  const dir = mkTmpDir();
  try {
    for (const name of ['.env', '.env.local', 'aws_credentials.json', 'server.pem', 'id_rsa', 'id_ed25519.pub']) {
      writeFileSync(join(dir, name), 'x');
      assert.throws(
        () => guardUploadPath(join(dir, name), MAX_UPLOAD_BYTES),
        /credential\/secret pattern/,
        `expected ${name} to be blocked`,
      );
    }
    // near-misses stay uploadable
    writeFileSync(join(dir, 'environment.pdf'), 'x');
    writeFileSync(join(dir, 'id_photo.jpg.pdf'), 'x');
    assert.ok(guardUploadPath(join(dir, 'environment.pdf'), MAX_UPLOAD_BYTES).path);
    assert.ok(guardUploadPath(join(dir, 'id_photo.jpg.pdf'), MAX_UPLOAD_BYTES).path);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('guardUploadPath: allows a normal file outside hidden dirs', () => {
  const dir = mkTmpDir();
  const f = join(dir, 'doc.pdf');
  writeFileSync(f, 'hello');
  try {
    const { path, size } = guardUploadPath(f, MAX_UPLOAD_BYTES);
    assert.ok(path.endsWith('doc.pdf'));
    assert.equal(size, 5);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

function mkTmpDir() {
  return mkdtempSync(join(tmpdir(), 'guard-'));
}

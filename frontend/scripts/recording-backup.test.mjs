import assert from 'node:assert/strict';
import { webcrypto } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import { IDBFactory, IDBKeyRange, IDBObjectStore } from 'fake-indexeddb';
import ts from 'typescript';

function load(name, globals = {}) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Blob, crypto: webcrypto, console, ...globals });
  return exports;
}

function fixture() {
  const held = new Set();
  const indexedDB = new IDBFactory();
  const navigator = { locks: {
    async request(name, options, callback) {
      if (held.has(name)) return callback(null);
      held.add(name);
      try { await callback({ name }); }
      finally { held.delete(name); }
    },
  } };
  return { ...load('browserRecordingBackup', { indexedDB, IDBKeyRange, navigator }), indexedDB };
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

test('reload restores ordered chunks, meeting identity and final notes', async () => {
  const { BrowserRecordingBackup, listBrowserRecordings } = fixture();
  const backup = await BrowserRecordingBackup.create('owner', 'Weekly meeting', '', 'audio/webm');
  await Promise.all(['first', 'second', 'third'].map((part) => backup.append(new Blob([part]))));
  await backup.update({ meetingId: 'draft-id', notes: 'Retained notes', finalized: true });
  const id = backup.metadata.id;
  backup.release();
  await tick();
  const rows = await listBrowserRecordings('owner');
  assert.equal(rows.length, 1);
  assert.equal(rows[0].chunkCount, 3);
  assert.equal(rows[0].meetingId, 'draft-id');
  const restored = await BrowserRecordingBackup.open('owner', id);
  try {
    const blob = await restored.readBlob();
    assert.equal(await blob.text(), 'firstsecondthird');
    assert.equal(blob.type, 'audio/webm');
    assert.equal(restored.metadata.notes, 'Retained notes');
    assert.equal(restored.metadata.finalized, true);
  } finally { restored.release(); }
});

test('account switches cannot list or open another account recording', async () => {
  const { BrowserRecordingBackup, listBrowserRecordings } = fixture();
  const backup = await BrowserRecordingBackup.create('first-owner', 'Private', '', 'audio/mp4');
  await backup.append(new Blob(['private']));
  backup.release();
  await tick();
  assert.equal((await listBrowserRecordings('second-owner')).length, 0);
  await assert.rejects(BrowserRecordingBackup.open('second-owner', backup.metadata.id), /찾을 수 없습니다/);
  assert.equal((await listBrowserRecordings('first-owner')).length, 1);
});

test('another tab cannot recover or delete a live recording', async () => {
  const { BrowserRecordingBackup } = fixture();
  const backup = await BrowserRecordingBackup.create('owner', 'Live', '', 'audio/webm');
  await assert.rejects(BrowserRecordingBackup.open('owner', backup.metadata.id), /다른 탭/);
  backup.release();
  await tick();
  const restored = await BrowserRecordingBackup.open('owner', backup.metadata.id);
  await restored.remove();
  await tick();
  await assert.rejects(BrowserRecordingBackup.open('owner', backup.metadata.id), /찾을 수 없습니다/);
});

test('quota failure preserves only the contiguous committed prefix and never reports later chunks saved', async () => {
  const { BrowserRecordingBackup } = fixture();
  const backup = await BrowserRecordingBackup.create('owner', 'Interrupted', '', 'audio/webm');
  await backup.append(new Blob(['saved']));
  const originalAdd = IDBObjectStore.prototype.add;
  IDBObjectStore.prototype.add = function (...args) {
    if (this.name === 'chunks') throw new DOMException('Disk full', 'QuotaExceededError');
    return originalAdd.apply(this, args);
  };
  try {
    await assert.rejects(backup.append(new Blob(['lost'])), /Disk full/);
    await assert.rejects(backup.append(new Blob(['must not append after a gap'])), /Disk full/);
    await assert.rejects(backup.update({ finalized: true }), /Disk full/);
  } finally {
    IDBObjectStore.prototype.add = originalAdd;
    backup.release();
  }
  await tick();
  const restored = await BrowserRecordingBackup.open('owner', backup.metadata.id);
  try {
    assert.equal(await (await restored.readBlob()).text(), 'saved');
    assert.equal(restored.metadata.finalized, false);
    assert.equal(restored.metadata.chunkCount, 1);
  } finally { restored.release(); }
});

test('upload receipt survives reload and cleanup removes both metadata and audio', async () => {
  const { BrowserRecordingBackup, listBrowserRecordings } = fixture();
  const backup = await BrowserRecordingBackup.create('owner', 'Uploaded', '', 'audio/ogg');
  await backup.append(new Blob(['audio']));
  await backup.update({ uploadKey: 'audio/owner/meeting/final.ogg' });
  backup.release();
  await tick();
  const restored = await BrowserRecordingBackup.open('owner', backup.metadata.id);
  assert.equal(restored.metadata.uploadKey, 'audio/owner/meeting/final.ogg');
  await restored.remove();
  assert.equal((await listBrowserRecordings('owner')).length, 0);
});

test('a released handle cannot delete a recording owned by a newer handle', async () => {
  const { BrowserRecordingBackup } = fixture();
  const first = await BrowserRecordingBackup.create('owner', 'Retained', '', 'audio/webm');
  await first.append(new Blob(['audio']));
  first.release();
  await tick();
  const current = await BrowserRecordingBackup.open('owner', first.metadata.id);
  try {
    await assert.rejects(first.remove(), /종료되었습니다/);
    assert.equal(await (await current.readBlob()).text(), 'audio');
  } finally { current.release(); }
});

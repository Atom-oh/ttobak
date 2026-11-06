import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function load(name) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

test('account members manage published meetings but only the creator deletes', () => {
  const { meetingCapabilities } = load('meetingManagement');
  assert.deepEqual({ ...meetingCapabilities({ userId: 'creator', canManage: true }, 'member') },
    { isCreator: false, canManage: true, canDelete: false });
  assert.deepEqual({ ...meetingCapabilities({ userId: 'creator', canManage: true }, 'creator') },
    { isCreator: true, canManage: true, canDelete: true });
  assert.equal(meetingCapabilities({ userId: 'creator', canManage: false }, 'reader').canManage, false);
  // An older API without canManage falls back to creator-only.
  assert.equal(meetingCapabilities({ userId: 'creator' }, 'member').canManage, false);
  assert.equal(meetingCapabilities({ userId: 'creator' }, 'creator').canManage, true);
  assert.equal(meetingCapabilities({ userId: 'creator' }, undefined).canDelete, false);
});

test('predecessor candidates are the creator\'s meetings only', () => {
  const { isPredecessorOwnerMatch } = load('meetingManagement');
  const own = { isShared: false };
  const creatorShared = { isShared: true, ownerUserId: 'creator' };
  const otherShared = { isShared: true, ownerUserId: 'someone' };
  // The creator picks from their own meetings, not others' shared ones.
  assert.equal(isPredecessorOwnerMatch(own, 'creator', true), true);
  assert.equal(isPredecessorOwnerMatch(otherShared, 'creator', true), false);
  // A managing member picks only meetings the creator shared with them.
  assert.equal(isPredecessorOwnerMatch(own, 'creator', false), false);
  assert.equal(isPredecessorOwnerMatch(creatorShared, 'creator', false), true);
  assert.equal(isPredecessorOwnerMatch(otherShared, 'creator', false), false);
  assert.equal(isPredecessorOwnerMatch({ isShared: true }, 'creator', false), false);
});

test('delete controls stay creator-only in the meeting detail page and header', () => {
  const detail = readFileSync(new URL('../src/app/meeting/[id]/MeetingDetailClient.tsx', import.meta.url), 'utf8');
  const header = readFileSync(new URL('../src/components/meeting/MeetingHeader.tsx', import.meta.url), 'utf8');
  assert.match(detail, /\{isCreator \? <MobileMoreMenu/);
  assert.match(header, /\{isCreator && <DesktopDeleteButton/);
  assert.match(header, /\{canManage && <MeetingShareButton/);
});

test('audio upload (and its KB promotion) is creator-only; managers keep file uploads', () => {
  const page = readFileSync(new URL('../src/app/meeting/[id]/MeetingDetailClient.tsx', import.meta.url), 'utf8');
  const gates = [...page.matchAll(/\{?(\w+) && !meeting\.audioCrop|showAudioUploader && (\w+)/g)].map((m) => m[1] ?? m[2]);
  assert.deepEqual(gates, ['isCreator', 'isCreator', 'isCreator']);
  assert.match(page, /showUploader && canUpload/);
});

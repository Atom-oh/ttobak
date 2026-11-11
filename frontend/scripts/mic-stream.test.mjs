import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

function loadMicStream() {
  const source = readFileSync(new URL('../src/lib/micStream.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports });
  return exports;
}

const { openMicStream, isMissingDeviceError } = loadMicStream();

function namedError(name) {
  const err = new Error(name === 'OverconstrainedError' ? 'Invalid constraint' : name);
  err.name = name;
  return err;
}

function fakeDevices(failExactWith) {
  const calls = [];
  return {
    calls,
    async getUserMedia(constraints) {
      calls.push(constraints);
      if (constraints.audio !== true && failExactWith) throw failExactWith;
      return { id: constraints.audio === true ? 'default' : 'exact' };
    },
  };
}

test('opens the saved device with an exact constraint', async () => {
  const devices = fakeDevices();
  const result = await openMicStream(devices, 'mic-1');
  assert.equal(result.stream.id, 'exact');
  assert.equal(result.usedDefault, false);
  assert.deepEqual(JSON.parse(JSON.stringify(devices.calls)), [{ audio: { deviceId: { exact: 'mic-1' } } }]);
});

test('opens the default microphone when no device is saved', async () => {
  const devices = fakeDevices();
  const result = await openMicStream(devices, '');
  assert.equal(result.stream.id, 'default');
  assert.equal(result.usedDefault, false);
  assert.equal(devices.calls.length, 1);
});

for (const name of ['OverconstrainedError', 'NotFoundError']) {
  test(`falls back to the default microphone on ${name}`, async () => {
    const devices = fakeDevices(namedError(name));
    const result = await openMicStream(devices, 'stale-id');
    assert.equal(result.stream.id, 'default');
    assert.equal(result.usedDefault, true);
    assert.equal(devices.calls.length, 2);
    assert.equal(devices.calls[1].audio, true);
  });
}

test('rethrows a denied permission without retrying', async () => {
  const denied = namedError('NotAllowedError');
  const devices = fakeDevices(denied);
  await assert.rejects(openMicStream(devices, 'mic-1'), (err) => err === denied);
  assert.equal(devices.calls.length, 1);
});

test('classifies only missing-device errors', () => {
  assert.equal(isMissingDeviceError(namedError('OverconstrainedError')), true);
  assert.equal(isMissingDeviceError(namedError('NotFoundError')), true);
  assert.equal(isMissingDeviceError(namedError('NotAllowedError')), false);
  assert.equal(isMissingDeviceError(null), false);
});

test('recording and preview use the fallback helper, not a raw exact constraint', () => {
  for (const path of ['../src/components/RecordButton.tsx', '../src/app/record/page.tsx']) {
    const source = readFileSync(new URL(path, import.meta.url), 'utf8');
    assert.match(source, /openMicStream\(navigator\.mediaDevices/, path);
    assert.doesNotMatch(source, /deviceId:\s*\{\s*exact:/, path);
  }
});

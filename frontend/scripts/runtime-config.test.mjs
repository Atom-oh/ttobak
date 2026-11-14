import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';

/** A fresh copy of the module (its cache is module state) with a scripted `/config.json`. */
function loadRuntimeConfig(responses, env = {}) {
  const source = readFileSync(new URL('../src/lib/runtimeConfig.ts', import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  const calls = { fetch: 0 };
  vm.runInNewContext(outputText, {
    exports,
    window: { location: { href: 'https://app.example/', host: 'app.example', protocol: 'https:', hostname: 'app.example' } },
    process: { env },
    URL,
    fetch: async () => {
      const next = responses[Math.min(calls.fetch++, responses.length - 1)];
      if (next instanceof Error) throw next;
      return next;
    },
  });
  return { getRuntimeConfig: exports.getRuntimeConfig, calls };
}

const served = { ok: true, json: async () => ({ cognito: { region: 'r', userPoolId: 'pool', userPoolClientId: 'client', identityPoolId: 'identity' } }) };

test('a served config is fetched once', async () => {
  const { getRuntimeConfig, calls } = loadRuntimeConfig([served]);
  assert.equal((await getRuntimeConfig()).cognito.identityPoolId, 'identity');
  await getRuntimeConfig();
  assert.equal(calls.fetch, 1);
});

test('a failed fetch with no usable fallback is not kept: the next call fetches again', async () => {
  const { getRuntimeConfig, calls } = loadRuntimeConfig([new Error('offline'), { ok: false }, served]);
  assert.equal((await getRuntimeConfig()).cognito.userPoolId, '');
  assert.equal((await getRuntimeConfig()).cognito.userPoolId, '');
  assert.equal((await getRuntimeConfig()).cognito.userPoolId, 'pool');
  await getRuntimeConfig();
  assert.equal(calls.fetch, 3);
});

test('concurrent calls during a failing fetch share it', async () => {
  const { getRuntimeConfig, calls } = loadRuntimeConfig([new Error('offline')]);
  await Promise.all([getRuntimeConfig(), getRuntimeConfig()]);
  assert.equal(calls.fetch, 1);
});

test('a usable build-time fallback is kept (local development without /config.json)', async () => {
  const { getRuntimeConfig, calls } = loadRuntimeConfig([{ ok: false }], {
    NEXT_PUBLIC_COGNITO_USER_POOL_ID: 'pool', NEXT_PUBLIC_COGNITO_CLIENT_ID: 'client',
  });
  assert.equal((await getRuntimeConfig()).cognito.userPoolId, 'pool');
  await getRuntimeConfig();
  assert.equal(calls.fetch, 1);
});

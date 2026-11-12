import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import { webcrypto } from 'node:crypto';

function load(name, globals = {}) {
  const source = readFileSync(new URL(`../src/lib/${name}.ts`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  vm.runInNewContext(outputText, { exports, Date, setTimeout, clearTimeout, AbortController, ...globals });
  return exports;
}

test('cards use the newest accessible meeting and actual topic frequency, with bounded text', () => {
  const { assistantCards, meetingQuestions } = load('assistantSuggestions');
  const cards = assistantCards([
    { meetingId: 'older', title: '지난 미팅', date: '2026-11-01', tags: ['예산', '예산'] },
    { meetingId: 'latest', title: '신제품 출시', date: '2026-11-11', tags: ['출시'] },
    { meetingId: 'second', title: '기획', date: '2026-11-10', tags: ['출시'] },
  ]);
  assert.equal(cards.length, 4);
  assert.match(cards[0].question, /신제품 출시/);
  assert.match(cards[1].question, /출시/);
  assert.ok(cards.every(card => card.question.length <= 220));
  assert.ok(assistantCards([]).every(card => !card.question.includes('EKS')));
  assert.ok(assistantCards([{ title: 'x'.repeat(10000), tags: ['y'.repeat(10000)] }])
    .every(card => card.question.length <= 220));
  assert.ok(meetingQuestions('영업 전략').every(question => question.includes('영업 전략')));
  assert.equal(meetingQuestions('').length, 3);
});

function detectorHarness() {
  let now = 0;
  let id = 0;
  const timers = new Map();
  const requests = [];
  const applied = [];
  const { LiveQuestionDetector } = load('liveQuestionDetector', {
    Date: { now: () => now },
    setTimeout: (fn, delay) => { timers.set(++id, { fn, at: now + delay }); return id; },
    clearTimeout: timer => timers.delete(timer),
  });
  const detector = new LiveQuestionDetector((input, signal) => new Promise((resolve, reject) => {
    requests.push({ input, signal, resolve, reject });
    signal?.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
  }), value => applied.push(value));
  async function advance(ms) {
    now += ms;
    for (const [timer, task] of [...timers]) {
      if (task.at <= now) { timers.delete(timer); task.fn(); }
    }
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  }
  return { detector, requests, applied, advance, timers };
}

test('a short question is detected without waiting for 50 words; rapid updates coalesce', async () => {
  const h = detectorHarness();
  h.detector.update({ transcript: '견적은 얼마인가요?', meetingTitle: '고객 제안', meetingContext: '예산 검토' });
  await h.advance(500);
  assert.equal(h.requests.length, 0);
  h.detector.update({ transcript: '견적은 얼마인가요? 납기는 언제인가요?', meetingTitle: '고객 제안' });
  await h.advance(1000);
  assert.equal(h.requests.length, 1);
  assert.match(h.requests[0].input.transcript, /납기/);
  h.requests[0].resolve({ questions: ['납기는?'], automatic: ['납기는?'] });
  await h.advance(0);
  assert.equal(h.applied.length, 1);
});

test('detection stays single-flight and schedules the latest input after its cooldown', async () => {
  const h = detectorHarness();
  h.detector.update({ transcript: '첫 번째 질문입니다?' });
  await h.advance(1500);
  h.detector.update({ transcript: '두 번째 질문입니다?' });
  await h.advance(1500);
  assert.equal(h.requests.length, 1);
  h.requests[0].resolve({ questions: [] });
  await h.advance(0);
  await h.advance(8499);
  assert.equal(h.requests.length, 1);
  await h.advance(1);
  assert.equal(h.requests.length, 2);
  assert.equal(h.requests[1].input.transcript, '두 번째 질문입니다?');
});

test('reset discards old responses, releases pending timers and allows the new recording', async () => {
  const h = detectorHarness();
  h.detector.update({ transcript: '이전 미팅의 질문입니다?' });
  await h.advance(1500);
  h.detector.reset();
  assert.equal(h.requests[0].signal.aborted, true);
  h.requests[0].resolve({ questions: ['이전 질문'] });
  await h.advance(0);
  assert.deepEqual(h.applied, []);
  h.detector.update({ transcript: '새 미팅의 질문입니다?' });
  await h.advance(1500);
  assert.equal(h.requests.length, 2);
  h.detector.dispose();
  h.requests[1].resolve({ questions: ['새 질문'] });
  await h.advance(0);
  assert.deepEqual(h.applied, []);
  assert.equal(h.timers.size, 0);
});

test('a stalled detection is aborted so the newest question can still be detected', async () => {
  const h = detectorHarness();
  h.detector.update({ transcript: '응답이 멈춘 질문입니다?' });
  await h.advance(1500);
  h.detector.update({ transcript: '최신 대화의 질문입니다?' });
  await h.advance(30000);
  assert.equal(h.requests[0].signal.aborted, true);
  await h.advance(1500);
  assert.equal(h.requests.length, 2);
  assert.equal(h.requests[1].input.transcript, '최신 대화의 질문입니다?');
  h.detector.dispose();
});

test('detection bounds UTF-16 text and recovers from a failed request on new input', async () => {
  const h = detectorHarness();
  h.detector.update({ transcript: '가'.repeat(20000), meetingTitle: '제안'.repeat(1000), meetingContext: '배경'.repeat(10000) });
  await h.advance(1500);
  assert.ok(h.requests[0].input.transcript.length <= 6000);
  assert.ok(h.requests[0].input.meetingTitle.length <= 120);
  assert.ok(h.requests[0].input.meetingContext.length <= 3000);
  h.requests[0].reject(new Error('offline'));
  await h.advance(0);
  h.detector.update({ transcript: '새로운 질문을 하겠습니다?' });
  await h.advance(10000);
  assert.equal(h.requests.length, 2);
});

test('automatic answering and external search have separate user-scoped preferences', () => {
  const storage = new Map();
  const { automaticAnswerStore, proactiveSearchStore, setProactiveSearchUser } = load('proactiveSearch', {
    localStorage: {
      getItem: key => storage.get(key) ?? null,
      setItem: (key, value) => storage.set(key, value),
      removeItem: key => storage.delete(key),
    },
  });
  assert.equal(automaticAnswerStore.get(), false);
  setProactiveSearchUser('alice');
  assert.equal(automaticAnswerStore.get(), true);
  assert.equal(proactiveSearchStore.get(), false);
  automaticAnswerStore.set(false);
  proactiveSearchStore.set(true);
  setProactiveSearchUser('bob');
  assert.equal(automaticAnswerStore.get(), true);
  assert.equal(proactiveSearchStore.get(), false);
  setProactiveSearchUser('alice');
  assert.equal(automaticAnswerStore.get(), false);
  assert.equal(proactiveSearchStore.get(), true);
  setProactiveSearchUser(null);
  assert.equal(automaticAnswerStore.get(), false);
});

test('automatic policy reaches both HTTP transports and manual asks keep their existing contract', async () => {
  const token = `header.${Buffer.from(JSON.stringify({ sub: 'alice', exp: Date.now() / 1000 + 3600 })).toString('base64url')}.sig`;
  for (const asynchronous of [true, false]) {
    const requests = [];
    const deps = {
      './accountListCache': load('accountListCache'),
      './auth': { getIdToken: () => token, refreshSession: async () => token },
      './runtimeConfig': { getRuntimeConfig: async () => ({ qaAsyncJobs: asynchronous }) },
      '@/components/auth/AuthProvider': { triggerAuthFailure() {} },
    };
    const { qaApi } = load('api', {
      require: id => { assert.ok(id in deps, id); return deps[id]; },
      atob, crypto: webcrypto, URLSearchParams, TextEncoder, TextDecoder, AbortController,
      process: { env: {} },
      fetch: async (url, init) => {
        if (init.method === 'POST') requests.push({ url, body: JSON.parse(init.body) });
        return new Response(JSON.stringify(asynchronous
          ? { jobId: init.method === 'GET' ? url.split('/').at(-1) : requests.at(-1).body.requestId,
              status: 'succeeded', result: { answer: '확인된 답변' } }
          : { answer: '확인된 답변' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
      },
    });
    await qaApi.ask('납기는?', '현재 대화', 'live-session', 'meeting', undefined,
      { automatic: true, allowWebSearch: false });
    assert.equal(requests[0].body.automatic, true);
    assert.equal(requests[0].body.allowWebSearch, false);
    await qaApi.ask('직접 질문');
    assert.equal('automatic' in requests[1].body, false);
    assert.equal('allowWebSearch' in requests[1].body, false);
  }
});

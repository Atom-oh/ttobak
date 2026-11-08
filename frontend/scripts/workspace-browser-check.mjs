import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';
import { createRequire } from 'node:module';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname, extname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const evidence = join(root, '.superpowers/sdd/2026-11-07-meeting-workspace-ui');
const output = resolve(process.env.WORKSPACE_CHECK_OUTPUT || join(evidence, 'task-4-browser'));
const preview = new URL(process.env.WORKSPACE_PREVIEW_URL || 'http://127.0.0.1:8771');
const baselineExport = resolve(process.env.WORKSPACE_BASELINE_EXPORT || join(evidence, 'baseline-export'));
const prototypePath = process.env.WORKSPACE_PROTOTYPE_PATH;
const widths = [360, 390, 768, 1024, 1440];
const themes = ['light', 'dark'];
const frozenNow = '2026-11-07T09:00:00.000Z';
const rowsSelector = 'a[href^="/meeting/"]';
const result = { fixtureRecords: 200, timezone: 'Asia/Seoul', locale: 'ko-KR', height: 1000, frozenNow, checks: [], metrics: [], screenshots: [] };

function isLoopback(url) {
  return ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname) && ['http:', 'https:'].includes(url.protocol);
}
assert.ok(isLoopback(preview), 'WORKSPACE_PREVIEW_URL must be loopback');

function installedPlaywright() {
  const candidates = [process.env.PLAYWRIGHT_PACKAGE_PATH, 'playwright', '@playwright/test'].filter(Boolean);
  for (const candidate of candidates) {
    try { return require(candidate); } catch (error) {
      if (error.code !== 'MODULE_NOT_FOUND') throw error;
    }
  }
  throw new Error('Set PLAYWRIGHT_PACKAGE_PATH to an existing Playwright package; this check never installs dependencies.');
}

const accounts = [
  { accountId: '11111111-1111-4111-8111-111111111111', name: 'Hanbit Commerce', role: 'owner', memberCount: 3 },
  { accountId: '22222222-2222-4222-8222-222222222222', name: 'Monolab', role: 'SSA', memberCount: 2 },
];
const [firstAccount, secondAccount] = accounts;
const inheritedAccount = {
  accountId: '33333333-3333-4333-8333-333333333333', name: 'Inherited Fixture',
  role: 'SSA', accessPermission: 'read', inheritedFromAccountId: firstAccount.accountId,
};
const meetings = Array.from({ length: 200 }, (_, index) => ({
  meetingId: `fixture-meeting-${String(index).padStart(3, '0')}`,
  userId: 'fixture-user',
  title: `검증용 고객 미팅 ${String(index).padStart(3, '0')}`,
  date: new Date(Date.UTC(2026, 10, 7, 9) - index * 3600000).toISOString(),
  createdAt: new Date(Date.UTC(2026, 10, 7, 9) - index * 3600000).toISOString(),
  updatedAt: new Date(Date.UTC(2026, 10, 7, 9) - index * 3600000).toISOString(),
  status: 'done', participants: ['검증 참가자'], tags: ['검증'],
  accountId: accounts[index % accounts.length].accountId,
  summary: '## 검증 요약\n\n고객의 요구 사항과 다음 미팅 준비를 정리합니다.\n\n- 일정 확인\n- 기술 검토\n'.repeat(8),
  duration: 1800, isShared: false,
}));
result.fixtureSha256 = createHash('sha256').update(JSON.stringify({ accounts, meetings })).digest('hex');

const calendarSource = {
  sourceId: 'b7aa27f9-9878-4f52-9b54-a3b73a941d31', provider: 'outlook', revision: 2,
  lastAttemptAt: frozenNow, lastCompletedAt: frozenNow, lastCompletedRevision: 2,
  completedWindowStart: '2026-11-01T00:00:00Z', completedWindowEnd: '2026-12-01T00:00:00Z',
};
const calendarEvents = [
  { eventId: 'ongoing', title: '검증 중인 개인 고객 대화', startsAt: '2026-11-07T08:30:00Z', endsAt: '2026-11-07T09:30:00Z', accountId: firstAccount.accountId },
  { eventId: 'next', title: '검증용 다음 개인 미팅', startsAt: '2026-11-08T01:00:00Z', endsAt: '2026-11-08T02:00:00Z', accountId: secondAccount.accountId },
  { eventId: 'all-day', title: '검증용 종일 일정', startsAt: '2026-11-07T15:00:00Z', endsAt: '2026-11-08T15:00:00Z', isAllDay: true, startDate: '2026-11-08', endDate: '2026-11-09' },
].map(event => ({ sourceId: calendarSource.sourceId, provider: 'outlook', timeZone: 'Asia/Seoul', isAllDay: false, ...event }));

function queueFinalFixAgenda(state, { isAllDay = false, finalCursor = null, expireFirst50 = false } = {}) {
  state.calendarMode = 'ready';
  const events = Array.from({ length: 51 }, (_, index) => ({
    sourceId: calendarSource.sourceId, provider: 'outlook', timeZone: 'Asia/Seoul', isAllDay,
    eventId: `finalfix-${isAllDay ? 'all-day' : 'timed'}-${String(index + 1).padStart(3, '0')}`,
    title: `FINALfix ${isAllDay ? 'all-day' : 'timed'} ${String(index + 1).padStart(3, '0')}`,
    startsAt: isAllDay ? '2026-11-07T00:00:00Z' : new Date(Date.parse(frozenNow) + index * 1000).toISOString(),
    endsAt: isAllDay ? '2026-11-08T00:00:00Z'
      : expireFirst50 && index < 50 ? '2026-11-07T09:30:00Z' : '2026-11-07T13:00:00Z',
    ...(isAllDay ? { startDate: '2026-11-07', endDate: '2026-11-08' } : {}),
  }));
  const pages = [
    { events: events.slice(0, 20).reverse(), nextCursor: 'finalfix-2' },
    { events: events.slice(20, 40).reverse(), nextCursor: 'finalfix-3' },
    { events: events.slice(40), nextCursor: finalCursor },
  ];
  const encodedBytes = pages.map(page => Buffer.byteLength(JSON.stringify(page)));
  assert.ok(encodedBytes.every(bytes => bytes < 14000));
  result.finalFixFixtures ??= [];
  result.finalFixFixtures.push({ isAllDay, expireFirst50, pageCounts: pages.map(page => page.events.length), encodedBytes });
  for (const page of pages) state.next('GET /api/calendar/events', { json: page });
  return events;
}

function insights(accountId) {
  return ['trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action', 'future-kind'].map((type, index) => ({
    type, text: `${accountId === firstAccount.accountId ? 'Hanbit' : 'Monolab'} 검증 인사이트 ${type}`,
    evidence: `검증용 근거 ${index}`, implication: '검증용 고객 맥락',
    nextAction: '검증용 다음 대화 준비', sourceType: 'meeting',
    sourceId: meetings[index].meetingId, occurredAt: frozenNow, tsMarker: '[TS:65]',
  }));
}

function deferred() {
  let release;
  const promise = new Promise(resolvePromise => { release = resolvePromise; });
  return { promise, release };
}

function fixture() {
  const queued = [];
  const calls = [];
  const pending = new Set();
  const unexpected = [];
  const blocked = [];
  const parents = new Map();
  const state = {
    calls, unexpected, blocked, queued, parents, pending,
    accounts: [...accounts, inheritedAccount], meetings: [...meetings], emptySections: false, calendarMode: 'setup',
    next(match, response) {
      const started = deferred();
      const finished = deferred();
      const entry = { match, response, started, finished };
      queued.push(entry);
      return entry;
    },
    hold(match, response) {
      const gate = deferred();
      const entry = state.next(match, async () => { await gate.promise; return response; });
      entry.release = gate.release;
      pending.add(entry);
      return entry;
    },
    fail(match, status = 500) {
      return state.next(match, { status, json: { error: { code: 'FIXTURE_FAILURE', message: 'Synthetic fixture failure' } } });
    },
  };
  state.respond = async route => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const call = { method, path: url.pathname, query: Object.fromEntries(url.searchParams), key: `${method} ${url.pathname}` };
    calls.push(call);
    const position = queued.findIndex(entry => typeof entry.match === 'function' ? entry.match(call) : entry.match === call.key);
    if (position >= 0) {
      const entry = queued.splice(position, 1)[0];
      entry.started.release(call);
      try {
        const response = typeof entry.response === 'function' ? await entry.response(call, route.request()) : entry.response;
        if (response) return await route.fulfill(response);
      } finally { entry.finished.release(); }
    }
    if (call.path === '/api/session/bootstrap') return route.fulfill({ json: {
      emailVerified: true, pendingGrants: 0, projectInvitationsEnabled: true, retryPending: false,
    } });
    if (call.path === '/api/accounts') return route.fulfill({ json: { accounts: state.accounts } });
    if (call.path === '/api/calendar/sources' || call.path === '/api/calendar/events') {
      assert.equal(method, 'GET', 'Browser calendar never syncs, shares or records');
      const sources = call.path.endsWith('/sources');
      if (state.calendarMode === 'unavailable') return route.fulfill({ status: 404, json: { error: { code: 'NOT_FOUND', message: 'Synthetic missing route' } } });
      if (!sources && state.calendarMode === 'failed') return route.fulfill({ status: 500, json: { error: { code: 'INTERNAL_ERROR', message: 'Synthetic calendar failure' } } });
      const progress = { ...calendarSource,
        ...(state.calendarMode === 'partial' ? { revision: 3 } : {}),
        ...(state.calendarMode === 'stale' ? { lastCompletedAt: '2026-11-07T08:00:00Z' } : {}),
      };
      const continued = state.calendarMode === 'pagination' && !call.query.cursor;
      const events = ['setup', 'empty'].includes(state.calendarMode) || continued ? []
        : calendarEvents.filter(event => !call.query.accountId || event.accountId === call.query.accountId);
      return route.fulfill({ json: sources
        ? { sources: state.calendarMode === 'setup' || continued ? [] : [progress], nextCursor: continued ? 'source-next' : null }
        : { events, nextCursor: continued ? 'event-next' : null } });
    }
    if (call.path === '/api/meetings' && method === 'GET') {
      const selected = (call.query.accountIds || call.query.accountId || '').split(',').filter(Boolean);
      let records = state.meetings.filter(meeting => !selected.length || selected.includes(meeting.accountId));
      if (call.query.tab === 'shared') records = records.slice(0, 2).map(meeting => ({ ...meeting, isShared: true }));
      if (call.query.limit) records = records.slice(0, Number(call.query.limit));
      return route.fulfill({ json: { meetings: records, nextCursor: records.length ? 'fixture-next' : null } });
    }
    if (/^\/api\/meetings\/fixture-meeting-\d+$/.test(call.path) && method === 'DELETE') {
      state.meetings = state.meetings.filter(meeting => !call.path.endsWith(meeting.meetingId));
      return route.fulfill({ status: 204 });
    }
    const match = /^\/api\/accounts\/([^/]+)(?:\/(.*))?$/.exec(call.path);
    if (match) {
      const [, accountId, section] = match;
      const account = state.accounts.find(item => item.accountId === accountId);
      if (!account) return route.fulfill({ status: 404, json: { error: { code: 'NOT_FOUND', message: 'Synthetic account missing' } } });
      if (section === 'parent' && method === 'PUT') {
        const parentAccountId = route.request().postDataJSON().parentAccountId;
        parents.set(accountId, parentAccountId);
        return route.fulfill({ json: { accountId, parentAccountId } });
      }
      if (!section) return route.fulfill({ json: {
        ...account, parentAccountId: parents.get(accountId), industry: 'Synthetic validation',
        ownerUserId: accountId === firstAccount.accountId ? 'fixture-user' : 'fixture-owner',
        members: account.accessPermission === 'read'
          ? [{ userId: 'fixture-owner', email: 'owner@example.invalid', role: 'owner' }]
          : [{ userId: 'fixture-user', email: 'fixture@example.invalid', role: account.role }],
      } });
      if (section === 'insights') return route.fulfill({ json: { insights: state.emptySections ? [] : insights(accountId) } });
      if (section === 'meetings') return route.fulfill({ json: { meetings: state.emptySections ? [] : state.meetings.map(meeting => ({
        meetingId: meeting.meetingId, title: meeting.title, date: meeting.date, ownerUserId: 'fixture-user',
      })) } });
      if (section === 'documents') return route.fulfill({ json: { documents: state.emptySections ? [] : [{
        docId: 'fixture-doc', accountId, title: '검증 문서', docType: 'note', createdAt: frozenNow, updatedAt: frozenNow,
      }] } });
      if (section === 'projects') return route.fulfill({ json: { projects: state.emptySections ? [] : [{ projectId: 'fixture-project', name: '검증 프로젝트', stage: 'Discovery' }] } });
      if (section === 'research') return route.fulfill({ json: { research: state.emptySections ? [] : [{
        researchId: 'fixture-research', topic: '검증 리서치', status: 'done', ownerUserId: 'fixture-user', createdAt: frozenNow,
      }] } });
    }
    unexpected.push(call);
    return route.fulfill({ status: 501, json: { error: { code: 'UNEXPECTED_FIXTURE_ROUTE', message: call.key } } });
  };
  return state;
}

function mappedPath(pathname) {
  if (pathname === '/') return '/index.html';
  const payload = pathname.match(/\/__next\.[^/]+\.txt$/)?.[0] || '';
  let route = payload ? pathname.slice(0, -payload.length) : pathname;
  route = route.replace(/^\/accounts\/[^/.]+\/docs\/[^/.]+/, '/accounts/_/docs/_');
  route = route.replace(/^\/(meeting|accounts|projects|docs)\/[^/.]+/, '/$1/_');
  if (payload) return route + payload;
  return extname(route) ? route : `${route.replace(/\/$/, '')}.html`;
}

async function staticServer(directory) {
  const server = createServer(async (request, response) => {
    try {
      const url = new URL(request.url, 'http://127.0.0.1');
      const path = resolve(directory, `.${mappedPath(decodeURIComponent(url.pathname))}`);
      assert.ok(path.startsWith(directory + sep));
      const body = await readFile(path);
      const types = { '.html': 'text/html', '.js': 'application/javascript', '.css': 'text/css', '.json': 'application/json', '.txt': 'text/x-component', '.woff2': 'font/woff2', '.svg': 'image/svg+xml' };
      response.writeHead(200, { 'Content-Type': types[extname(path)] || 'application/octet-stream', 'Cache-Control': 'no-store' });
      response.end(body);
    } catch { response.writeHead(404); response.end('Not found'); }
  });
  await new Promise(resolveListening => server.listen(0, '127.0.0.1', resolveListening));
  return { origin: `http://127.0.0.1:${server.address().port}`, close: () => new Promise(resolveClosed => {
    server.close(resolveClosed);
    server.closeAllConnections();
  }) };
}

async function session(browser, origin, width = 1440, theme = 'light', state = fixture()) {
  const context = await browser.newContext({
    viewport: { width, height: 1000 }, timezoneId: result.timezone, locale: result.locale,
    colorScheme: theme, reducedMotion: 'reduce', serviceWorkers: 'block', deviceScaleFactor: 1,
  });
  const page = await context.newPage();
  page.setDefaultTimeout(7000);
  const errors = [];
  const pendingRequests = new Set();
  page.on('request', request => pendingRequests.add(request.url()));
  page.on('requestfinished', request => pendingRequests.delete(request.url()));
  page.on('requestfailed', request => pendingRequests.delete(request.url()));
  page.on('pageerror', error => errors.push(error.message));
  await page.clock.setFixedTime(new Date(frozenNow));
  await context.routeWebSocket('**/*', route => {
    state.blocked.push('websocket');
    route.close();
  });
  const token = ['eyJhbGciOiJub25lIn0', Buffer.from(JSON.stringify({
    sub: 'fixture-user', email: 'fixture@example.invalid', name: 'Fixture User', email_verified: true,
    exp: 2000000000, iat: 1794000000, token_use: 'id',
  })).toString('base64url'), 'fixture'].join('.');
  await context.addInitScript(({ token, theme }) => {
    const prefix = 'CognitoIdentityServiceProvider.fixtureclient.fixture@example.invalid';
    localStorage.setItem('CognitoIdentityServiceProvider.fixtureclient.LastAuthUser', 'fixture@example.invalid');
    for (const name of ['idToken', 'accessToken']) localStorage.setItem(`${prefix}.${name}`, token);
    localStorage.setItem(`${prefix}.clockDrift`, '0');
    localStorage.setItem(`${prefix}.refreshToken`, 'fixture-refresh');
    localStorage.setItem('idToken', token);
    localStorage.setItem('theme', theme);
    const originalFetch = window.fetch.bind(window);
    window.fetch = async (...args) => {
      const response = await originalFetch(...args);
      if (String(args[0]).includes('/api/meetings')) window.fixtureMeetingResponseAt = performance.now();
      return response;
    };
    window.fixtureMaxRows = 0;
    const observer = new MutationObserver(() => {
      const rows = document.querySelectorAll('a[href^="/meeting/"]').length;
      window.fixtureMaxRows = Math.max(window.fixtureMaxRows, rows);
      if (rows && window.fixtureMeetingResponseAt && window.fixtureRowsRenderedMs === undefined) {
        window.fixtureRowsRenderedMs = performance.now() - window.fixtureMeetingResponseAt;
      }
    });
    observer.observe(document, { childList: true, subtree: true });
  }, { token, theme });
  await context.route('**/*', async route => {
    const url = new URL(route.request().url());
    if (!isLoopback(url)) {
      state.blocked.push(`${url.protocol}//${url.hostname}`);
      return route.abort();
    }
    if (url.pathname === '/config.json') return route.fulfill({ json: { cognito: {
      region: 'us-east-1', userPoolId: 'us-east-1_fixture', userPoolClientId: 'fixtureclient', identityPoolId: '',
    }, billingService: false } });
    if (url.pathname.startsWith('/api/')) return state.respond(route);
    const mapped = mappedPath(url.pathname);
    if (mapped !== url.pathname) {
      const response = await route.fetch({ url: `${url.origin}${mapped}${url.search}`, maxRedirects: 0 });
      return route.fulfill({ response });
    }
    return route.continue();
  });
  return { context, page, state, errors, pendingRequests, origin, width, theme };
}

async function settle(page) {
  await page.evaluate(() => new Promise(resolveFrame => requestAnimationFrame(() => requestAnimationFrame(resolveFrame))));
}
async function visible(locator) {
  await locator.waitFor({ state: 'visible' });
}
async function count(locator, expected) {
  await locator.page().waitForFunction(({ selector, expected }) => document.querySelectorAll(selector).length === expected,
    { selector: rowsSelector, expected });
  assert.equal(await locator.count(), expected);
}
async function screenshot(current, name) {
  await settle(current.page);
  const path = join(output, `${name}.png`);
  await current.page.screenshot({ path, fullPage: false, animations: 'disabled' });
  result.screenshots.push(path);
}
async function geometry(current) {
  const measured = await current.page.evaluate(() => {
    const main = document.querySelector('main');
    return {
      pageWidth: document.documentElement.scrollWidth, viewport: innerWidth,
      mainWidth: main?.clientWidth, mainScrollWidth: main?.scrollWidth,
      rows: document.querySelectorAll('a[href^="/meeting/"]').length, maxRows: window.fixtureMaxRows,
    };
  });
  assert.ok(measured.pageWidth <= measured.viewport + 1, JSON.stringify(measured));
  assert.ok(measured.mainScrollWidth <= measured.mainWidth + 1, JSON.stringify(measured));
  assert.ok(measured.rows <= 100 && measured.maxRows <= 100, JSON.stringify(measured));
  return measured;
}
async function lastRow(current, name) {
  const last = current.page.locator(rowsSelector).last();
  await last.scrollIntoViewIfNeeded();
  const box = await last.boundingBox();
  assert.ok(box && box.y >= 0 && box.y + box.height <= 1000, 'Last row must be reachable in internal scroll container');
  assert.ok(await current.page.locator('main').evaluate(element => element.scrollTop > 0));
  await screenshot(current, name);
}
async function keyboardFocus(current, locator, name) {
  await locator.scrollIntoViewIfNeeded();
  await locator.focus();
  await current.page.keyboard.press('Tab');
  await current.page.keyboard.press('Shift+Tab');
  assert.ok(await locator.evaluate(element => document.activeElement === element));
  const focus = await locator.evaluate(element => {
    const style = getComputedStyle(element);
    return { visible: element.matches(':focus-visible'), outline: style.outlineStyle, width: style.outlineWidth, shadow: style.boxShadow };
  });
  assert.ok(focus.visible && ((focus.outline !== 'none' && parseFloat(focus.width) > 0) || focus.shadow !== 'none'), JSON.stringify(focus));
  await screenshot(current, name);
}

async function waitForGate(promise) {
  let timer;
  try {
    return await Promise.race([promise, new Promise((resolveWait, rejectWait) => {
      timer = setTimeout(() => rejectWait(new Error('Expected fixture request did not settle within 7 seconds')), 7000);
    })]);
  } finally { clearTimeout(timer); }
}

async function popupContrast(current) {
  const contrast = await current.page.getByLabel('어카운트 검색', { exact: true }).evaluate(input => {
    const panel = input.closest('[id]');
    const label = panel.querySelector('[role="group"] button');
    const canvas = document.createElement('canvas');
    const drawing = canvas.getContext('2d');
    const luminance = color => {
      drawing.fillStyle = color;
      drawing.fillRect(0, 0, 1, 1);
      const channels = [...drawing.getImageData(0, 0, 1, 1).data].slice(0, 3).map(value => {
        const normalized = value / 255;
        return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
      });
      return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722;
    };
    const foreground = getComputedStyle(label).color;
    const background = getComputedStyle(panel).backgroundColor;
    const lightness = [luminance(foreground), luminance(background)].sort((left, right) => left - right);
    return { foreground, background, ratio: (lightness[1] + 0.05) / (lightness[0] + 0.05) };
  });
  assert.ok(contrast.ratio >= 4.5, `Account popup label is unreadable: ${JSON.stringify(contrast)}`);
}

let browser;
let baselineServer;
let activeSession;
const filter = process.env.WORKSPACE_CHECK_FILTER ? new RegExp(process.env.WORKSPACE_CHECK_FILTER) : null;

async function check(name, execute) {
  if (filter && !filter.test(name)) return;
  try {
    await execute();
    if (activeSession) {
      assert.deepEqual(activeSession.errors, [], 'Unexpected browser exception');
      assert.deepEqual(activeSession.state.unexpected, [], 'Unmocked API route');
      assert.deepEqual(activeSession.state.blocked, [], 'Unexpected external request (blocked before network)');
    }
    result.checks.push({ name, status: 'passed' });
    console.log(`PASS ${name}`);
  } catch (error) {
    result.checks.push({ name, status: 'failed', error: error.stack,
      pendingRequests: activeSession ? [...activeSession.pendingRequests] : [],
      unexpected: activeSession?.state.unexpected, browserErrors: activeSession?.errors });
    if (activeSession) await screenshot(activeSession, `failure-${name}`).catch(() => {});
    console.error(`FAIL ${name}: ${error.message}`);
  } finally {
    if (activeSession) {
      for (const entry of activeSession.state.pending) entry.release?.();
      await activeSession.context.unrouteAll({ behavior: 'ignoreErrors' });
      await activeSession.context.close();
      activeSession = null;
    }
    await writeFile(join(output, 'results.json'), JSON.stringify(result, null, 2));
  }
}

async function open(path, width = 1440, theme = 'light', state = fixture(), origin = preview.origin) {
  activeSession = await session(browser, origin, width, theme, state);
  await activeSession.page.goto(`${origin}${path}`, { waitUntil: 'domcontentloaded' });
  return activeSession;
}

async function meetingActions(current) {
  const { page, state } = current;
  const rows = page.locator(rowsSelector);
  await count(rows, 100);
  const initialCalls = state.calls.filter(call => call.path === '/api/meetings').length;
  await page.getByRole('button', { name: '시간순', exact: true }).click();
  assert.equal(await rows.first().getAttribute('href'), '/meeting/fixture-meeting-000');
  await page.getByLabel('미팅 정렬', { exact: true }).selectOption('oldest');
  assert.equal(await rows.first().getAttribute('href'), '/meeting/fixture-meeting-199');
  await page.getByRole('button', { name: '다음 100개', exact: true }).click();
  assert.equal(await rows.first().getAttribute('href'), '/meeting/fixture-meeting-099');
  await count(rows, 100);
  assert.equal(state.calls.filter(call => call.path === '/api/meetings').length, initialCalls, 'Display window must not fetch server data');
  await page.getByRole('button', { name: '이전 100개', exact: true }).click();
  await page.getByLabel('미팅 정렬', { exact: true }).selectOption('newest');
  await page.getByRole('textbox', { name: '미팅 검색', exact: true }).fill('고객 미팅 199');
  await count(rows, 1);
  assert.equal(new URL(page.url()).searchParams.get('q'), '고객 미팅 199');
  await page.getByRole('textbox', { name: '미팅 검색', exact: true }).fill('no-fixture-match');
  await count(rows, 0);
  await visible(page.getByText('불러온 결과에 조건에 맞는 미팅이 없습니다', { exact: true }));
  await visible(page.getByRole('button', { name: 'Load More', exact: true }));
  await page.getByRole('button', { name: '검색·필터 해제', exact: true }).first().click();
  await page.getByLabel('미팅 상태', { exact: true }).selectOption('error');
  await count(rows, 0);
  await page.getByLabel('미팅 상태', { exact: true }).selectOption('');
  await page.getByLabel('시작일', { exact: true }).fill('2026-11-07');
  await page.getByLabel('종료일', { exact: true }).fill('2026-11-07');
  await count(rows, 19);
  await page.getByRole('button', { name: '검색·필터 해제', exact: true }).first().click();
  await page.getByRole('button', { name: '최근 7일', exact: true }).click();
  await visible(page.getByText('불러온 200개 미팅 · 조건 일치 169개', { exact: true }));
  await page.getByRole('button', { name: '전체 미팅', exact: true }).click();
  await page.getByRole('button', { name: '공유받은 미팅', exact: true }).click();
  await count(rows, 2);
  assert.equal(await page.getByRole('button', { name: /미팅 삭제$/ }).count(), 0);
  await page.getByRole('button', { name: '전체 미팅', exact: true }).click();
  await count(rows, 100);
  await page.getByRole('button', { name: '고객별', exact: true }).click();
  assert.equal(await page.getByRole('region', { name: 'Hanbit Commerce', exact: true }).count(), 1);
  await page.getByRole('button', { name: '전체 어카운트', exact: true }).click();
  await screenshot(current, `account-filter-${current.width}-${current.theme}`);
  await popupContrast(current);
  await page.getByRole('checkbox', { name: firstAccount.name, exact: true }).check();
  await page.keyboard.press('Escape');
  await visible(page.getByText('불러온 100개 미팅 · 조건 일치 100개', { exact: true }));
  await page.getByRole('button', { name: '어카운트 해제', exact: true }).click();
  await visible(page.getByText('불러온 200개 미팅 · 조건 일치 200개', { exact: true }));
  await geometry(current);
}

async function chooseAccount(current, account) {
  const selector = current.page.getByRole('complementary', { name: '고객 선택', exact: true });
  if (current.width < 1024) await selector.getByLabel('고객 선택', { exact: true }).selectOption(account.accountId);
  else await selector.getByRole('button', { name: new RegExp(account.name) }).click();
  await visible(current.page.getByRole('heading', { name: account.name, exact: true }));
}

async function main() {
  await mkdir(output, { recursive: true });
  const { chromium } = installedPlaywright();
  browser = await chromium.launch({
    executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH,
    headless: true,
    args: ['--disable-background-networking', '--disable-component-update', '--disable-domain-reliability', '--no-proxy-server', '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1'],
  });
  result.browser = browser.version();
  baselineServer = await staticServer(baselineExport);
  for (let run = 1; run <= 3; run++) {
    for (const variant of ['baseline', 'implementation']) {
      await check(`performance-${variant}-${run}`, async () => {
        const state = fixture();
        state.accounts = accounts;
        const origin = variant === 'baseline' ? baselineServer.origin : preview.origin;
        const started = performance.now();
        const current = await open(variant === 'baseline' ? '/' : '/meetings', 1440, 'light', state, origin);
        await count(current.page.locator(rowsSelector), variant === 'baseline' ? 200 : 100);
        await current.page.evaluate(() => document.fonts.ready);
        await settle(current.page);
        const navigationToLoadedMs = performance.now() - started;
        const metrics = await current.page.evaluate(() => ({
          nodes: document.querySelectorAll('*').length,
          rows: document.querySelectorAll('a[href^="/meeting/"]').length,
          responseToFirstRowMs: window.fixtureRowsRenderedMs,
        }));
        assert.ok(Number.isFinite(metrics.responseToFirstRowMs));
        result.metrics.push({ variant, run, viewport: '1440x1000', navigationToLoadedMs, ...metrics });
        await screenshot(current, `performance-${variant}-${run}`);
      });
    }
  }
  for (const width of widths) for (const theme of themes) {
    const suffix = `${width}-${theme}`;
    await check(`prototype-${suffix}`, async () => {
      assert.ok(prototypePath, 'Set WORKSPACE_PROTOTYPE_PATH to the approved local HTML');
      const current = await open('/', width, theme);
      await current.page.route('**/prototype.html', route => readFile(prototypePath).then(body => route.fulfill({ contentType: 'text/html', body })));
      await current.page.goto(`${preview.origin}/prototype.html`);
      if (theme === 'dark') await current.page.locator('#theme-toggle').click();
      for (const view of ['home', 'meetings', 'accounts']) {
        await current.page.locator(`.nav-item[data-page="${view}"]`).click();
        await screenshot(current, `prototype-${view}-${suffix}`);
      }
    });
    await check(`meetings-${suffix}`, async () => {
      const current = await open('/meetings', width, theme);
      await count(current.page.locator(rowsSelector), 100);
      assert.equal(await current.page.locator('html').evaluate(element => element.classList.contains('dark')), theme === 'dark');
      await geometry(current);
      await screenshot(current, `meetings-${suffix}`);
      await keyboardFocus(current, current.page.getByRole('button', { name: '전체 미팅', exact: true }), `meeting-focus-${suffix}`);
      await keyboardFocus(current, current.page.locator(rowsSelector).first(), `meeting-row-focus-${suffix}`);
      await lastRow(current, `meetings-last-row-${suffix}`);
      await meetingActions(current);
    });
    await check(`home-${suffix}`, async () => {
      const current = await open('/', width, theme);
      await count(current.page.locator(rowsSelector), 11);
      await visible(current.page.getByText('Outlook 일정을 연결해 보세요.', { exact: true }));
      assert.equal(current.state.calls.filter(call => call.path.endsWith('/insights')).length, 1, 'Home must not fan out');
      await screenshot(current, `home-${suffix}`);
      await current.page.getByLabel('인사이트 어카운트', { exact: true }).selectOption(secondAccount.accountId);
      await visible(current.page.getByRole('heading', { name: 'Monolab 검증 인사이트 trend', exact: true }));
      await current.page.getByLabel('인사이트 어카운트', { exact: true }).selectOption(firstAccount.accountId);
      await visible(current.page.getByRole('heading', { name: 'Hanbit 검증 인사이트 trend', exact: true }));
      await geometry(current);
      await lastRow(current, `home-recent-scroll-${suffix}`);
      const recent = current.page.getByRole('region', { name: '불러온 미팅의 최근 기록', exact: true });
      assert.equal(await recent.locator(rowsSelector).count(), 6);
    });
    await check(`accounts-${suffix}`, async () => {
      const current = await open(`/accounts/${firstAccount.accountId}?keep=fixture`, width, theme);
      const { page, state } = current;
      await visible(page.getByRole('heading', { name: firstAccount.name, exact: true }));
      await visible(page.getByLabel('상위 어카운트', { exact: true }));
      assert.equal(state.calls.filter(call => /\/(insights|meetings|projects|documents|research)$/.test(call.path)).length, 0);
      await screenshot(current, `accounts-overview-${suffix}`);
      const overview = page.getByRole('tab', { name: '개요', exact: true });
      await keyboardFocus(current, overview, `account-focus-${suffix}`);
      await page.keyboard.press('ArrowRight');
      await visible(page.getByRole('heading', { name: 'Shared Meetings', exact: true }));
      assert.equal(await page.getByRole('tab', { name: '미팅', exact: true }).getAttribute('aria-selected'), 'true');
      await count(page.locator(rowsSelector), 100);
      await screenshot(current, `accounts-meetings-${suffix}`);
      await page.getByRole('button', { name: '다음', exact: true }).click();
      assert.equal(await page.locator(rowsSelector).first().getAttribute('href'), '/meeting/fixture-meeting-100');
      await page.getByRole('tab', { name: '인사이트', exact: true }).click();
      await visible(page.getByRole('heading', { name: 'Hanbit 검증 인사이트 future-kind', exact: true }));
      assert.equal(await page.locator('article').count(), 9);
      await screenshot(current, `accounts-insights-${suffix}`);
      await page.getByRole('button', { name: '리스크 1', exact: true }).click();
      assert.equal(await page.locator('article').count(), 1);
      await visible(page.getByText('검증용 근거 3', { exact: true }));
      await visible(page.locator('article').filter({ hasText: '검증용 다음 대화 준비' }));
      await page.getByRole('tab', { name: '프로젝트', exact: true }).click();
      await visible(page.getByText('검증 프로젝트', { exact: true }));
      await visible(page.getByText('검증 리서치', { exact: true }));
      await page.getByRole('tab', { name: '문서', exact: true }).click();
      await visible(page.getByText('검증 문서', { exact: true }));
      await geometry(current);
      assert.equal(await page.getByRole('tab', { name: 'Billing', exact: true }).count(), 0);
      assert.equal(new URL(page.url()).searchParams.get('keep'), 'fixture');
      await chooseAccount(current, inheritedAccount);
      await visible(page.getByText('상위 어카운트에서 공유됨 · 읽기 전용', { exact: true }));
      assert.equal(await page.getByRole('button', { name: /Note|Slide/ }).count(), 0);
      await page.getByRole('tab', { name: '개요', exact: true }).click();
      assert.equal(await page.getByLabel('상위 어카운트', { exact: true }).count(), 0);
      assert.equal(await page.getByPlaceholder('Search colleague by name or email').count(), 0);
      await screenshot(current, `accounts-inherited-${suffix}`);
    });
  }

  await check('meeting-loading-error-empty-pagination', async () => {
    const state = fixture();
    const gate = state.hold('GET /api/meetings', { status: 500, json: { error: { code: 'FIXTURE', message: 'Synthetic failure' } } });
    const current = await open('/meetings', 390, 'dark', state);
    const { page } = current;
    await waitForGate(gate.started.promise);
    await visible(page.getByText('미팅 목록을 불러오는 중…', { exact: true }));
    await screenshot(current, 'meeting-initial-loading');
    gate.release();
    await visible(page.getByText('미팅 목록을 불러오지 못했습니다.', { exact: true }));
    await screenshot(current, 'meeting-initial-error');
    await page.getByRole('button', { name: '다시 시도', exact: true }).click();
    await count(page.locator(rowsSelector), 100);
    state.fail(call => call.path === '/api/meetings' && Boolean(call.query.cursor));
    await page.getByRole('button', { name: 'Load More', exact: true }).click();
    await visible(page.getByText('추가 미팅을 불러오지 못했습니다.', { exact: true }));
    await count(page.locator(rowsSelector), 100);
    await screenshot(current, 'meeting-pagination-error-retained');
    state.next(call => call.path === '/api/meetings' && Boolean(call.query.cursor), { json: {
      meetings: [meetings[0], { ...meetings[0], meetingId: 'fixture-added', title: '검증 추가 미팅' }], nextCursor: null,
    } });
    await page.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(page.getByText('불러온 201개 미팅 · 조건 일치 201개', { exact: true }));
    assert.equal(await page.getByRole('button', { name: 'Load More', exact: true }).count(), 0);
    await geometry(current);
    state.meetings = [];
    await page.reload();
    await visible(page.getByText('아직 미팅이 없습니다', { exact: true }));
    await screenshot(current, 'meeting-genuine-empty');
  });

  await check('account-discovery-error-retry', async () => {
    const state = fixture();
    state.fail('GET /api/accounts');
    const current = await open('/meetings', 390, 'light', state);
    await count(current.page.locator(rowsSelector), 100);
    await visible(current.page.getByText('어카운트 목록을 불러오지 못했습니다.', { exact: true }));
    await current.page.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(current.page.getByRole('heading', { name: 'Hanbit Commerce', exact: true }));
  });

  await check('meeting-account-switch-race', async () => {
    const current = await open('/meetings');
    const { page, state } = current;
    await count(page.locator(rowsSelector), 100);
    const gate = state.hold(call => call.path === '/api/meetings' && call.query.accountIds === firstAccount.accountId,
      { json: { meetings: [{ ...meetings[0], title: 'STALE ACCOUNT RESPONSE' }], nextCursor: null } });
    await page.getByRole('button', { name: '전체 어카운트', exact: true }).click();
    await page.getByRole('checkbox', { name: firstAccount.name, exact: true }).check();
    await waitForGate(gate.started.promise);
    await count(page.locator(rowsSelector), 0);
    await page.getByRole('button', { name: '전체 해제', exact: true }).click();
    await page.getByRole('checkbox', { name: secondAccount.name, exact: true }).check();
    await page.keyboard.press('Escape');
    await count(page.locator(rowsSelector), 100);
    gate.release();
    await waitForGate(gate.finished.promise);
    await settle(page);
    assert.equal(await page.getByText('STALE ACCOUNT RESPONSE', { exact: true }).count(), 0);
    assert.equal(await page.locator(rowsSelector).first().getAttribute('href'), '/meeting/fixture-meeting-001');
    await screenshot(current, 'meeting-account-race');
  });

  await check('home-independent-error-retry-and-race', async () => {
    const state = fixture();
    state.fail(`GET /api/accounts/${firstAccount.accountId}/insights`);
    const current = await open('/', 390, 'dark', state);
    const { page } = current;
    const recent = page.getByRole('region', { name: '불러온 미팅의 최근 기록', exact: true });
    await visible(page.getByRole('button', { name: '인사이트 다시 시도', exact: true }));
    assert.equal(await recent.locator(rowsSelector).count(), 6);
    await screenshot(current, 'home-independent-insights-error');
    await page.getByRole('button', { name: '인사이트 다시 시도', exact: true }).click();
    await visible(page.getByRole('heading', { name: 'Hanbit 검증 인사이트 trend', exact: true }));
    const gate = state.hold(`GET /api/accounts/${secondAccount.accountId}/insights`, { json: { insights: [{ ...insights(secondAccount.accountId)[0], text: 'STALE INSIGHT RESPONSE' }] } });
    await page.getByLabel('인사이트 어카운트', { exact: true }).selectOption(secondAccount.accountId);
    await waitForGate(gate.started.promise);
    await visible(page.getByText('인사이트를 불러오는 중…', { exact: true }));
    await page.getByLabel('인사이트 어카운트', { exact: true }).selectOption(firstAccount.accountId);
    await visible(page.getByRole('heading', { name: 'Hanbit 검증 인사이트 trend', exact: true }));
    gate.release();
    await waitForGate(gate.finished.promise);
    await settle(page);
    assert.equal(await page.getByText('STALE INSIGHT RESPONSE', { exact: true }).count(), 0);
    state.fail('GET /api/meetings');
    await page.reload();
    await visible(page.getByRole('button', { name: '최근 기록 다시 시도', exact: true }));
    await visible(page.getByRole('heading', { name: 'Hanbit 검증 인사이트 trend', exact: true }));
    await page.getByRole('button', { name: '최근 기록 다시 시도', exact: true }).click();
    await visible(recent.locator(rowsSelector).first());
    state.meetings = [];
    state.emptySections = true;
    await page.reload();
    await visible(page.getByText('아직 확인할 미팅 기록이 없습니다. 녹음하거나 음성 파일을 업로드해 보세요.', { exact: true }));
    await visible(page.getByText('아직 추출된 인사이트가 없습니다.', { exact: true }));
    await screenshot(current, 'home-genuine-empty');
  });

  await check('account-sections-error-retry-empty-and-race', async () => {
    const current = await open(`/accounts/${firstAccount.accountId}?tab=insights`, 390, 'dark');
    const { page, state } = current;
    await visible(page.getByRole('heading', { name: 'Hanbit 검증 인사이트 trend', exact: true }));
    state.fail(`GET /api/accounts/${secondAccount.accountId}/insights`);
    await chooseAccount(current, secondAccount);
    await visible(page.getByRole('alert'));
    await screenshot(current, 'account-insights-error');
    await page.getByRole('tab', { name: '미팅', exact: true }).click();
    await count(page.locator(rowsSelector), 100);
    await page.getByRole('tab', { name: '인사이트', exact: true }).click();
    await visible(page.getByRole('heading', { name: 'Monolab 검증 인사이트 trend', exact: true }));
    await page.getByRole('tab', { name: '개요', exact: true }).click();
    const gate = state.hold(`GET /api/accounts/${secondAccount.accountId}/insights`, { json: {
      insights: [{ ...insights(secondAccount.accountId)[0], text: 'STALE DETAIL RESPONSE' }],
    } });
    await page.getByRole('tab', { name: '인사이트', exact: true }).click();
    await waitForGate(gate.started.promise);
    await chooseAccount(current, firstAccount);
    await visible(page.getByRole('heading', { name: 'Hanbit 검증 인사이트 trend', exact: true }));
    gate.release();
    await waitForGate(gate.finished.promise);
    await settle(page);
    assert.equal(await page.getByText('STALE DETAIL RESPONSE', { exact: true }).count(), 0);
    state.emptySections = true;
    for (const [tab, empty] of [['미팅', 'No shared meetings.'], ['인사이트', '아직 추출된 인사이트가 없습니다.'], ['프로젝트', '연결된 프로젝트가 없습니다.'], ['문서', 'No documents.']]) {
      await page.getByRole('tab', { name: tab, exact: true }).click();
      await visible(page.getByText(empty, { exact: true }));
    }
    state.fail(`GET /api/accounts/${firstAccount.accountId}/insights`);
    await page.getByRole('tab', { name: '인사이트', exact: true }).click();
    await visible(page.getByRole('alert'));
    await page.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(page.getByText('아직 추출된 인사이트가 없습니다.', { exact: true }));
    await screenshot(current, 'account-section-empty-after-retry');
  });

  await check('parent-save-survives-tab-switch', async () => {
    const current = await open(`/accounts/${firstAccount.accountId}`);
    const { page, state } = current;
    const selector = page.getByLabel('상위 어카운트', { exact: true });
    await visible(selector);
    await selector.selectOption(secondAccount.accountId);
    const gate = state.hold(`PUT /api/accounts/${firstAccount.accountId}/parent`, undefined);
    await page.getByRole('button', { name: '저장', exact: true }).click();
    await waitForGate(gate.started.promise);
    await page.getByRole('tab', { name: '미팅', exact: true }).click();
    await count(page.locator(rowsSelector), 100);
    gate.release();
    await waitForGate(gate.finished.promise);
    await page.getByRole('tab', { name: '개요', exact: true }).click();
    await visible(selector);
    assert.equal(await selector.inputValue(), secondAccount.accountId);
    assert.ok(await page.getByRole('button', { name: '저장', exact: true }).isDisabled());
    await screenshot(current, 'parent-save-after-tab-switch');
  });

  await check('legacy-search-and-record-actions', async () => {
    const current = await open('/?q=고객%20미팅%20199&keep=fixture', 390, 'light');
    const { page } = current;
    await page.waitForURL('**/meetings?**');
    await count(page.locator(rowsSelector), 1);
    assert.equal(new URL(page.url()).searchParams.get('keep'), 'fixture');
    assert.equal(await page.getByRole('link', { name: '녹음 시작', exact: true }).getAttribute('href'), '/record');
    assert.equal(await page.getByRole('link', { name: '음성 파일 업로드', exact: true }).getAttribute('href'), '/record?mode=upload');
    await keyboardFocus(current, page.getByRole('link', { name: '녹음 시작', exact: true }), 'record-action-focus');
  });

  await check('empty-account-discovery', async () => {
    const state = fixture();
    state.accounts = [];
    const current = await open('/', 360, 'light', state);
    await visible(current.page.getByText('접근할 수 있는 어카운트가 없습니다.', { exact: false }));
    assert.ok(await current.page.getByLabel('인사이트 어카운트', { exact: true }).isDisabled());
    assert.equal(state.calls.filter(call => call.path.endsWith('/insights')).length, 0);
    await screenshot(current, 'home-no-accounts');
    await current.page.goto(`${preview.origin}/meetings`);
    await visible(current.page.getByText('사용 가능한 어카운트가 없습니다.', { exact: true }));
    await count(current.page.locator(rowsSelector), 100);
    await geometry(current);
  });

  await check('account-initial-loading-error-retry-access-loss', async () => {
    const state = fixture();
    const gate = state.hold(`GET /api/accounts/${firstAccount.accountId}`, {
      status: 500, json: { error: { code: 'FIXTURE_FAILURE', message: 'Synthetic account load failure' } },
    });
    const current = await open(`/accounts/${firstAccount.accountId}`, 390, 'light', state);
    const { page } = current;
    await waitForGate(gate.started.promise);
    await visible(page.getByRole('status', { name: '어카운트 불러오는 중', exact: true }));
    await screenshot(current, 'account-initial-loading');
    gate.release();
    await visible(page.getByRole('alert'));
    await screenshot(current, 'account-initial-error');
    await page.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(page.getByRole('heading', { name: firstAccount.name, exact: true }));
    state.fail(`GET /api/accounts/${firstAccount.accountId}/insights`, 403);
    await page.getByRole('tab', { name: '인사이트', exact: true }).click();
    await visible(page.getByText('어카운트 접근 권한을 확인할 수 없습니다. 다시 시도해 주세요.', { exact: false }));
    assert.equal(await page.getByRole('heading', { name: firstAccount.name, exact: true }).count(), 0);
    assert.equal(await page.getByRole('tablist').count(), 0);
    await screenshot(current, 'account-access-lost');
  });

  await check('delete-failure-retry-and-preview-text', async () => {
    const state = fixture();
    state.meetings = [{ ...meetings[0], summary: '<img src="invalid" onerror="window.fixtureInjected=true"> **Synthetic preview**' }];
    const current = await open('/meetings', 390, 'dark', state);
    const { page } = current;
    await count(page.locator(rowsSelector), 1);
    assert.equal(await page.locator(`${rowsSelector} img`).count(), 0);
    assert.equal(await page.evaluate(() => window.fixtureInjected), undefined);
    page.on('dialog', dialog => dialog.accept());
    state.fail(`DELETE /api/meetings/${meetings[0].meetingId}`);
    const remove = page.getByRole('button', { name: '검증용 고객 미팅 000 미팅 삭제', exact: true });
    await remove.click();
    await visible(page.getByText('미팅 삭제에 실패했습니다. 다시 시도해 주세요.', { exact: true }));
    await count(page.locator(rowsSelector), 1);
    await screenshot(current, 'meeting-delete-error');
    await remove.click();
    await count(page.locator(rowsSelector), 0);
  });

  for (const width of widths) for (const theme of themes) {
    await check(`calendar-layout-${width}-${theme}`, async () => {
      const state = fixture();
      state.calendarMode = 'ready';
      const current = await open('/', width, theme, state);
      const upcoming = current.page.getByRole('region', { name: '내 Outlook 일정', exact: true });
      await visible(upcoming.getByRole('heading', { name: '검증 중인 개인 고객 대화', exact: true }).first());
      await visible(upcoming.getByRole('heading', { name: '종일 일정', exact: true }));
      await visible(upcoming.getByText('원본 시간대: Asia/Seoul', { exact: true }).first());
      assert.equal(state.calls.filter(call => call.path === '/api/calendar/events').length, 1);
      await geometry(current);
      await screenshot(current, `calendar-layout-${width}-${theme}`);
      assert.equal(await current.page.evaluate(() => [...Object.values(localStorage), ...Object.values(sessionStorage)]
        .some(value => /검증 중인 개인 고객 대화|검증용 다음 개인 미팅|검증용 종일 일정/.test(value))), false);
    });
  }
  await check('calendar-distinct-states-and-independent-history', async () => {
    const state = fixture();
    const current = await open('/meetings', 390, 'dark', state);
    const { page } = current;
    const upcoming = page.getByRole('region', { name: '내 Outlook 일정', exact: true });
    for (const [mode, text] of [
      ['setup', 'Outlook 일정을 연결해 보세요.'],
      ['unavailable', '일정 동기화를 아직 사용할 수 없습니다.'],
      ['empty', '보고된 동기화 범위에 일정이 없습니다.'],
      ['partial', '현재 변경 사항의 동기화 진행이 완료되지 않았습니다.'],
      ['stale', '오래된 동기화 보고입니다.'],
      ['failed', '일정을 불러오지 못했습니다.'],
    ]) {
      state.calendarMode = mode;
      await page.reload({ waitUntil: 'domcontentloaded' });
      await visible(upcoming.getByText(text, { exact: false }).first());
      await count(page.locator(rowsSelector), 100);
      if (mode !== 'setup') await screenshot(current, `calendar-state-${mode}`);
    }
    state.calendarMode = 'ready';
    await upcoming.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(upcoming.getByRole('heading', { name: '검증 중인 개인 고객 대화', exact: true }).first());
    assert.ok(state.calls.filter(call => call.path.startsWith('/api/calendar/')).every(call => call.method === 'GET'));
  });
  await check('calendar-empty-pagination-source-and-event-retry', async () => {
    const state = fixture();
    state.calendarMode = 'pagination';
    const current = await open('/', 390, 'light', state);
    const { page } = current;
    const upcoming = page.getByRole('region', { name: '내 Outlook 일정', exact: true });
    const moreEvents = upcoming.getByRole('button', { name: '일정 더 불러오기', exact: true });
    const moreSources = upcoming.getByRole('button', { name: '동기화 소스 더 불러오기', exact: true });
    await visible(moreEvents);
    await visible(moreSources);
    assert.equal(await upcoming.getByText('보고된 동기화 범위에 일정이 없습니다.', { exact: true }).count(), 0);
    assert.equal(state.calls.filter(call => call.path.startsWith('/api/calendar/')).length, 2);
    await keyboardFocus(current, moreSources, 'calendar-source-pagination-focus');
    state.fail('GET /api/calendar/events');
    await moreEvents.click();
    await visible(upcoming.getByText('추가 일정을 불러오지 못했습니다.', { exact: false }));
    await moreEvents.click();
    await visible(upcoming.getByRole('heading', { name: '검증 중인 개인 고객 대화', exact: true }).first());
    await moreSources.click();
    await visible(upcoming.getByText('마지막 완료 보고:', { exact: false }));
    assert.equal(await moreEvents.count(), 0);
    assert.equal(await moreSources.count(), 0);
    await screenshot(current, 'calendar-pagination-drained');
    const eventCalls = state.calls.filter(call => call.path === '/api/calendar/events');
    assert.deepEqual(eventCalls.slice(1).map(call => call.query.cursor), ['event-next', 'event-next']);
    assert.equal(new Set(eventCalls.map(call => `${call.query.from}/${call.query.to}`)).size, 1);
  });
  await check('calendar-pagination-denial-retires-sibling-and-retry-recovers', async () => {
    const state = fixture();
    state.calendarMode = 'ready';
    state.next('GET /api/calendar/events', { json: { events: [calendarEvents[0]], nextCursor: 'event-next' } });
    state.next('GET /api/calendar/sources', { json: { sources: [calendarSource], nextCursor: 'source-next' } });
    const current = await open('/', 390, 'light', state);
    const { page } = current;
    const upcoming = page.getByRole('region', { name: '내 Outlook 일정', exact: true });
    await visible(upcoming.getByRole('heading', { name: calendarEvents[0].title, exact: true }).first());
    const sibling = state.hold('GET /api/calendar/events', { json: { events: [calendarEvents[1]], nextCursor: null } });
    const aborted = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/api/calendar/events');
    await upcoming.getByRole('button', { name: '일정 더 불러오기', exact: true }).click();
    await waitForGate(sibling.started.promise);
    state.fail('GET /api/calendar/sources', 403);
    await upcoming.getByRole('button', { name: '동기화 소스 더 불러오기', exact: true }).click();
    await visible(upcoming.getByText('일정을 불러오지 못했습니다.', { exact: false }));
    await aborted;
    sibling.release();
    await waitForGate(sibling.finished.promise);
    assert.equal(await upcoming.getByRole('heading', { name: calendarEvents[0].title, exact: true }).count(), 0);
    assert.equal(await upcoming.getByRole('heading', { name: calendarEvents[1].title, exact: true }).count(), 0);
    assert.equal(await upcoming.getByRole('button', { name: /더 불러오기/ }).count(), 0);
    await screenshot(current, 'calendar-denied-pagination-cleared');
    const callsBeforeRetry = state.calls.filter(call => call.path.startsWith('/api/calendar/')).length;
    await upcoming.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(upcoming.getByRole('heading', { name: calendarEvents[0].title, exact: true }).first());
    const retried = state.calls.filter(call => call.path.startsWith('/api/calendar/')).slice(callsBeforeRetry);
    assert.equal(retried.length, 2);
    assert.ok(retried.every(call => !call.query.cursor));
    await screenshot(current, 'calendar-denied-pagination-fresh-retry');
  });
  await check('calendar-stalled-sources-denial-invalidates-parent-before-recovery', async () => {
    const state = fixture();
    state.calendarMode = 'ready';
    const current = await open(`/accounts/${firstAccount.accountId}`, 1024, 'light', state);
    const { page } = current;
    const selector = page.getByLabel('상위 어카운트', { exact: true });
    await visible(selector);
    await selector.selectOption(secondAccount.accountId);
    const saved = state.hold(`PUT /api/accounts/${firstAccount.accountId}/parent`,
      { json: { accountId: firstAccount.accountId, parentAccountId: secondAccount.accountId } });
    await page.getByRole('button', { name: '저장', exact: true }).click();
    await waitForGate(saved.started.promise);
    const sibling = state.hold('GET /api/calendar/sources', { json: { sources: [calendarSource], nextCursor: null } });
    const denial = state.hold('GET /api/calendar/events',
      { status: 403, json: { error: { code: 'FORBIDDEN', message: 'Synthetic access removed' } } });
    await page.getByRole('tab', { name: '미팅', exact: true }).click();
    await waitForGate(sibling.started.promise);
    await waitForGate(denial.started.promise);
    denial.release();
    await waitForGate(denial.finished.promise);
    await visible(page.getByText('어카운트 접근 권한을 확인할 수 없습니다. 다시 시도해 주세요.', { exact: false }));
    assert.equal(await page.getByRole('tablist').count(), 0);
    await page.getByRole('button', { name: '다시 시도', exact: true }).click();
    await visible(page.getByRole('heading', { name: firstAccount.name, exact: true }));
    saved.release();
    await waitForGate(saved.finished.promise);
    sibling.release();
    await waitForGate(sibling.finished.promise);
    await page.getByRole('tab', { name: '개요', exact: true }).click();
    await visible(selector);
    assert.equal(await selector.inputValue(), '');
    await screenshot(current, 'calendar-stalled-source-parent-remains-invalid');
  });
  await check('calendar-account-lifetime-denial-and-private-filter', async () => {
    const state = fixture();
    state.calendarMode = 'ready';
    const gate = state.hold(call => call.path === '/api/calendar/events' && call.query.accountId === firstAccount.accountId,
      { json: { events: [calendarEvents[0]], nextCursor: null } });
    const current = await open(`/accounts/${firstAccount.accountId}`, 768, 'light', state);
    const { page } = current;
    await visible(page.getByRole('heading', { name: firstAccount.name, exact: true }));
    assert.equal(state.calls.filter(call => call.path.startsWith('/api/calendar/')).length, 0);
    await page.getByRole('tab', { name: '미팅', exact: true }).click();
    await waitForGate(gate.started.promise);
    await visible(page.getByText('일정을 불러오는 중…', { exact: true }));
    await chooseAccount(current, secondAccount);
    await visible(page.getByRole('heading', { name: '검증용 다음 개인 미팅', exact: true }).first());
    gate.release();
    await waitForGate(gate.finished.promise);
    assert.equal(await page.getByRole('heading', { name: '검증 중인 개인 고객 대화', exact: true }).count(), 0);
    const callsBeforeInsights = state.calls.filter(call => call.path.startsWith('/api/calendar/')).length;
    await page.getByRole('tab', { name: '인사이트', exact: true }).click();
    await visible(page.getByRole('heading', { name: 'Monolab 검증 인사이트 trend', exact: true }));
    assert.equal(state.calls.filter(call => call.path.startsWith('/api/calendar/')).length, callsBeforeInsights);
    state.fail('GET /api/calendar/events', 403);
    await page.getByRole('tab', { name: '미팅', exact: true }).click();
    await visible(page.getByText('어카운트 접근 권한을 확인할 수 없습니다. 다시 시도해 주세요.', { exact: false }));
    assert.equal(await page.getByRole('tablist').count(), 0);
    assert.equal(await page.getByRole('heading', { name: '검증용 다음 개인 미팅', exact: true }).count(), 0);
    assert.equal(await page.getByText('일정 동기화를 아직 사용할 수 없습니다.', { exact: false }).count(), 0);
    await screenshot(current, 'calendar-account-denied');
  });
  await check('calendar-clock-advances-with-stable-window', async () => {
    const state = fixture();
    state.calendarMode = 'ready';
    const current = await open('/', 1024, 'dark', state);
    const { page } = current;
    await page.clock.install({ time: new Date(frozenNow) });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await visible(page.getByRole('heading', { name: '검증 중인 개인 고객 대화', exact: true }).first());
    const countBefore = state.calls.filter(call => call.path.startsWith('/api/calendar/')).length;
    await page.clock.fastForward(31 * 60_000);
    await visible(page.getByText('오래된 동기화 보고입니다.', { exact: false }));
    assert.equal(await page.getByRole('heading', { name: '검증 중인 개인 고객 대화', exact: true }).count(), 0);
    assert.equal(state.calls.filter(call => call.path.startsWith('/api/calendar/')).length, countBefore);
    await screenshot(current, 'calendar-advancing-clock-stale');
  });

  for (const isAllDay of [false, true]) {
    await check(`finalfix-agenda-${isAllDay ? 'all-day' : 'timed'}-51`, async () => {
      const state = fixture();
      const events = queueFinalFixAgenda(state, { isAllDay });
      const current = await open('/', isAllDay ? 1440 : 390, isAllDay ? 'light' : 'dark', state);
      const { page } = current;
      const kind = isAllDay ? '종일' : '시간 지정';
      const agenda = page.getByRole('heading', { name: '조회 기간 일정', exact: true }).locator('..');
      const more = agenda.getByRole('button', { name: '일정 더 불러오기', exact: true });
      await visible(agenda.getByText('불러온 일정 20개', { exact: false }));
      assert.equal(await agenda.locator('article').count(), 20);
      await more.click();
      await visible(agenda.getByText('불러온 일정 40개', { exact: false }));
      assert.equal(await agenda.locator('article').count(), 40);
      await more.click();
      await visible(agenda.getByText('불러온 일정 51개', { exact: false }));
      assert.equal(await more.count(), 0);
      assert.deepEqual(await agenda.locator('article h3').allTextContents(), events.slice(0, 50).map(event => event.title));
      const previous = agenda.getByRole('button', { name: `${kind} 일정 이전 페이지`, exact: true });
      const next = agenda.getByRole('button', { name: `${kind} 일정 다음 페이지`, exact: true });
      assert.ok(await previous.isDisabled());
      await keyboardFocus(current, next, `finalfix-agenda-${isAllDay ? 'all-day' : 'timed'}-focus`);
      await page.keyboard.press('Enter');
      await visible(agenda.getByRole('heading', { name: events[50].title, exact: true }));
      assert.deepEqual(await agenda.locator('article h3').allTextContents(), [events[50].title]);
      assert.ok(await next.isDisabled());
      await visible(agenda.getByText(`${kind} 일정 51–51 / 51개`, { exact: true }));
      if (!isAllDay) await visible(page.getByRole('heading', { name: events[0].title, exact: true }));
      await previous.click();
      assert.deepEqual(await agenda.locator('article h3').allTextContents(), events.slice(0, 50).map(event => event.title));
      await next.click();
      assert.deepEqual(await agenda.locator('article h3').allTextContents(), [events[50].title]);
      assert.deepEqual(state.calls.filter(call => call.path === '/api/calendar/events').map(call => call.query.cursor),
        [undefined, 'finalfix-2', 'finalfix-3']);
      assert.equal(state.calls.filter(call => call.path === '/api/calendar/sources').length, 1);
      await agenda.scrollIntoViewIfNeeded();
      await geometry(current);
      await screenshot(current, `finalfix-agenda-${isAllDay ? 'all-day' : 'timed'}-last`);
    });
  }

  await check('finalfix-agenda-failed-empty-pages-and-clock-clamp', async () => {
    const state = fixture();
    const current = await open('/', 390, 'light', state);
    const { page } = current;
    await page.clock.install({ time: new Date(frozenNow) });
    const events = queueFinalFixAgenda(state, { finalCursor: 'finalfix-empty', expireFirst50: true });
    const before = state.calls.length;
    await page.reload({ waitUntil: 'domcontentloaded' });
    const agenda = page.getByRole('heading', { name: '조회 기간 일정', exact: true }).locator('..');
    const more = agenda.getByRole('button', { name: '일정 더 불러오기', exact: true });
    await visible(agenda.getByText('불러온 일정 20개', { exact: false }));
    await more.click();
    await visible(agenda.getByText('불러온 일정 40개', { exact: false }));
    await more.click();
    await visible(agenda.getByText('불러온 일정 51개', { exact: false }));
    const previous = agenda.getByRole('button', { name: '시간 지정 일정 이전 페이지', exact: true });
    const next = agenda.getByRole('button', { name: '시간 지정 일정 다음 페이지', exact: true });
    await next.click();
    state.fail('GET /api/calendar/events');
    await more.click();
    await visible(agenda.getByText('추가 일정을 불러오지 못했습니다.', { exact: false }));
    assert.deepEqual(await agenda.locator('article h3').allTextContents(), [events[50].title]);
    await screenshot(current, 'finalfix-agenda-pagination-failure-retained');
    await previous.click();
    assert.equal(await agenda.locator('article').count(), 50);
    await next.click();
    state.next('GET /api/calendar/events', { json: { events: [], nextCursor: 'finalfix-empty-last' } });
    await more.click();
    await visible(more);
    assert.equal(await agenda.getByRole('alert').count(), 0);
    state.next('GET /api/calendar/events', { json: { events: [], nextCursor: null } });
    await more.click();
    await more.waitFor({ state: 'detached' });
    assert.deepEqual(await agenda.locator('article h3').allTextContents(), [events[50].title]);
    assert.deepEqual(state.calls.slice(before).filter(call => call.path === '/api/calendar/events').map(call => call.query.cursor),
      [undefined, 'finalfix-2', 'finalfix-3', 'finalfix-empty', 'finalfix-empty', 'finalfix-empty-last']);
    const beforeClock = state.calls.length;
    await page.clock.fastForward(31 * 60_000);
    await visible(agenda.getByText('불러온 일정 1개', { exact: false }));
    assert.deepEqual(await agenda.locator('article h3').allTextContents(), [events[50].title]);
    assert.equal(await previous.count(), 0);
    assert.equal(await next.count(), 0);
    await visible(page.getByText('오래된 동기화 보고입니다.', { exact: false }));
    assert.equal(state.calls.length, beforeClock);
    await agenda.scrollIntoViewIfNeeded();
    await geometry(current);
    await screenshot(current, 'finalfix-agenda-clock-clamped');
  });

  await check('finalfix-home-delete-confirm-retry-backfill-and-empty', async () => {
    const state = fixture();
    state.meetings = meetings.slice(0, 7).map((meeting, index) => ({ ...meeting, isShared: index === 1 }));
    const current = await open('/', 390, 'dark', state);
    const { page } = current;
    const recent = page.getByRole('region', { name: '불러온 미팅의 최근 기록', exact: true });
    await visible(recent.getByText('불러온 7개 중 최근 6개', { exact: false }));
    assert.equal(await recent.locator(rowsSelector).count(), 6);
    assert.equal(await recent.getByRole('button', { name: '검증용 고객 미팅 001 미팅 삭제', exact: true }).count(), 0);
    let confirmDelete = false;
    page.on('dialog', dialog => confirmDelete ? dialog.accept() : dialog.dismiss());
    const remove = recent.getByRole('button', { name: '검증용 고객 미팅 000 미팅 삭제', exact: true });
    await remove.click();
    await settle(page);
    assert.equal(state.calls.filter(call => call.method === 'DELETE').length, 0);
    confirmDelete = true;
    state.fail(`DELETE /api/meetings/${meetings[0].meetingId}`);
    await remove.click();
    await visible(recent.getByText('미팅 삭제에 실패했습니다. 다시 시도해 주세요.', { exact: true }));
    assert.equal(await recent.locator(rowsSelector).count(), 6);
    await visible(recent.getByText('불러온 7개 중 최근 6개', { exact: false }));
    await screenshot(current, 'finalfix-home-delete-failure');
    await remove.click();
    await remove.waitFor({ state: 'detached' });
    assert.equal(await recent.locator(rowsSelector).count(), 6);
    assert.deepEqual(await recent.locator(rowsSelector).evaluateAll(rows => rows.map(row => row.getAttribute('href'))),
      meetings.slice(1, 7).map(meeting => `/meeting/${meeting.meetingId}`));
    await visible(recent.getByText('불러온 6개 중 최근 6개', { exact: false }));
    assert.equal(await recent.getByRole('alert').count(), 0);
    assert.equal(state.calls.filter(call => call.path === '/api/meetings').length, 1);
    await recent.scrollIntoViewIfNeeded();
    await screenshot(current, 'finalfix-home-delete-backfilled');
    state.meetings = [meetings[0]];
    await page.reload();
    await visible(remove);
    assert.equal(await recent.locator(rowsSelector).count(), 1);
    await remove.click();
    await visible(recent.getByText('불러온 0개 중 최근 0개', { exact: false }));
    assert.equal(await recent.locator(rowsSelector).count(), 0);
    await visible(recent.getByText('아직 확인할 미팅 기록이 없습니다.', { exact: false }));
    assert.equal(state.calls.filter(call => call.path === '/api/meetings').length, 2, 'only initial load and explicit reload list records');
    assert.equal(state.calls.filter(call => call.method === 'DELETE').length, 3);
    await recent.scrollIntoViewIfNeeded();
    await geometry(current);
    await screenshot(current, 'finalfix-home-delete-empty');
  });
}

try {
  await main();
} finally {
  await activeSession?.context.unrouteAll({ behavior: 'ignoreErrors' });
  await activeSession?.context.close();
  await browser?.close();
  await baselineServer?.close();
}
assert.ok(result.checks.length, 'No checks selected');
const failed = result.checks.filter(check => check.status === 'failed');
console.log(`${result.checks.length - failed.length}/${result.checks.length} checks passed; evidence: ${output}`);
if (failed.length) process.exitCode = 1;

import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { posix } from 'node:path';
import { test } from 'node:test';
import vm from 'node:vm';
import ts from 'typescript';
import * as React from 'react';
import * as jsxRuntime from 'react/jsx-runtime';
import { renderToStaticMarkup } from 'react-dom/server';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

function loadWorkspace(query = '', hooks = {}, environment = {}) {
  const cache = new Map();
  let currentUrl = new URL(`/?${query}`, 'https://workspace.invalid');
  const dependencies = {
    react: { ...React, ...hooks },
    'react/jsx-runtime': jsxRuntime,
    'react-markdown': { default: ReactMarkdown },
    'remark-gfm': { default: remarkGfm },
    'next/link': { default: ({ children, ...props }) => React.createElement('a', props, children) },
    'next/navigation': {
      useSearchParams: () => new URLSearchParams(currentUrl.search),
      usePathname: () => currentUrl.pathname,
      ...environment.navigation,
    },
    '@/lib/api': { meetingsApi: { delete: async () => {} } },
  };
  function load(path) {
    if (path in dependencies) return dependencies[path];
    if (cache.has(path)) return cache.get(path);
    const relative = path.replace(/^@\//, '');
    let source;
    for (const extension of ['ts', 'tsx']) {
      try {
        source = readFileSync(new URL(`../src/${relative}.${extension}`, import.meta.url), 'utf8');
        break;
      } catch (error) {
        if (error.code !== 'ENOENT' || extension === 'tsx') throw error;
      }
    }
    const { outputText } = ts.transpileModule(source, {
      compilerOptions: {
        module: ts.ModuleKind.CommonJS,
        target: ts.ScriptTarget.ES2022,
        jsx: ts.JsxEmit.ReactJSX,
      },
    });
    const exports = {};
    cache.set(path, exports);
    vm.runInNewContext(outputText, {
      exports,
      console,
      URLSearchParams,
      setTimeout,
      clearTimeout,
      window: {
        get location() { return currentUrl; },
        history: { replaceState: (_state, _title, href) => { currentUrl = new URL(href, currentUrl); } },
      },
      ...environment.globals,
      require: name => load(name.startsWith('.') ? posix.join(posix.dirname(path), name) : name),
    });
    return exports;
  }
  return load;
}

const defaults = { sort: 'newest', query: '', statuses: [] };
const meeting = (meetingId, date = '2026-11-07T12:00:00Z', extra = {}) => ({
  PK: 'USER#viewer', SK: `MEETING#${meetingId}`, meetingId, userId: 'viewer',
  title: `미팅 ${meetingId}`, date, status: 'done', createdAt: date, updatedAt: date,
  ...extra,
});
const ids = rows => Array.from(rows, row => row.meetingId);
const listProps = {
  meetings: [], activeTab: 'all', onTabChange() {},
  selectedAccountIds: [], onAccountChange() {}, accounts: [],
  isLoadingAccounts: false, accountsError: null, onRetryAccounts() {},
  hasMore: false, error: null, onRetry() {}, onDeleteMeeting() {},
};
const renderList = (props = {}, query = '') => {
  const { MeetingList } = loadWorkspace(query)('@/components/MeetingList');
  return renderToStaticMarkup(React.createElement(MeetingList, { ...listProps, ...props }));
};

function searchInteraction(initialUrl = '/meetings?q=alpha') {
  const history = [new URL(initialUrl, 'https://workspace.invalid')];
  const replacements = [];
  let historyIndex = 0;
  const timers = new Map();
  let timerId = 0;
  let activeFrame;
  let slotIndex = 0;
  const frame = () => ({ slots: [], dirty: false, effects: [] });
  const listFrame = frame();
  const headerFrame = frame();
  const currentUrl = () => history[historyIndex];
  const replace = href => {
    history[historyIndex] = new URL(href, currentUrl());
    replacements.push(href);
  };
  const push = href => {
    history.splice(historyIndex + 1, Infinity, new URL(href, currentUrl()));
    historyIndex++;
  };
  const load = loadWorkspace('', {
    useState(initial) {
      const owner = activeFrame;
      const slot = slotIndex++;
      if (!(slot in owner.slots)) owner.slots[slot] = typeof initial === 'function' ? initial() : initial;
      return [owner.slots[slot], value => {
        const next = typeof value === 'function' ? value(owner.slots[slot]) : value;
        if (!Object.is(next, owner.slots[slot])) {
          owner.slots[slot] = next;
          owner.dirty = true;
        }
      }];
    },
    useMemo: callback => callback(),
    useRef(initial) {
      const slot = slotIndex++;
      if (!(slot in activeFrame.slots)) activeFrame.slots[slot] = { current: initial };
      return activeFrame.slots[slot];
    },
    useEffect(callback, dependencies) {
      const owner = activeFrame;
      const slot = slotIndex++;
      const previous = owner.slots[slot];
      if (!previous || dependencies.some((value, index) => !Object.is(value, previous.dependencies[index]))) {
        owner.slots[slot] = { dependencies };
        owner.effects.push(() => {
          previous?.cleanup?.();
          owner.slots[slot].cleanup = callback();
        });
      }
    },
  }, {
    navigation: {
      useSearchParams: () => new URLSearchParams(currentUrl().search),
      usePathname: () => currentUrl().pathname,
      useRouter: () => ({ replace, push }),
    },
    globals: {
      window: {
        get location() { return currentUrl(); },
        history: { replaceState: (_state, _title, href) => replace(href) },
      },
      setTimeout: callback => { timers.set(++timerId, callback); return timerId; },
      clearTimeout: handle => timers.delete(handle),
    },
  });
  const flatten = tree => {
    if (!tree || typeof tree !== 'object') return [];
    if (Array.isArray(tree)) return tree.flatMap(flatten);
    return [tree, ...flatten(tree.props?.children)];
  };
  const { MeetingList } = load('@/components/MeetingList');
  const { DesktopHeader } = load('@/components/layout/DesktopHeader');
  const HeaderSearch = flatten(DesktopHeader({})).find(element => element.type?.name === 'HeaderSearch').type;
  const render = (component, owner, props = {}) => {
    let tree;
    let attempts = 0;
    do {
      assert.ok(attempts++ < 25, 'component render should settle');
      activeFrame = owner;
      slotIndex = 0;
      owner.dirty = false;
      tree = component(props);
    } while (owner.dirty);
    owner.effects.splice(0).forEach(effect => effect());
    return flatten(tree);
  };
  const elements = control => control === 'header'
    ? render(HeaderSearch, headerFrame)
    : render(MeetingList, listFrame, {
      ...listProps, meetings: [meeting('alpha'), meeting('beta'), meeting('alpha-beta', undefined, { title: 'alpha beta 검토' })],
    });
  const input = control => elements(control).find(element => element.props?.['aria-label'] === '미팅 검색');
  const refresh = () => { elements('list'); elements('header'); };
  refresh();
  return {
    change(control, value) {
      input(control).props.onChange({ target: { value } });
      refresh();
    },
    submitHeader(isComposing = false) {
      input('header').props.onKeyDown({ key: 'Enter', nativeEvent: { isComposing } });
      refresh();
    },
    selectStatus(value) {
      elements('list').find(element => element.props?.['aria-label'] === '미팅 상태').props.onChange({ target: { value } });
      refresh();
    },
    clear() {
      elements('list').find(element => element.type === 'button' && element.props.children === '검색·필터 해제').props.onClick();
      refresh();
    },
    navigate(href) { push(href); refresh(); },
    back() { historyIndex--; refresh(); },
    forward() { historyIndex++; refresh(); },
    historyReplacements() { return [...replacements]; },
    flushTimers() {
      const pending = [...timers.values()];
      timers.clear();
      pending.forEach(callback => callback());
      refresh();
    },
    snapshot() {
      return {
        href: `${currentUrl().pathname}${currentUrl().search}${currentUrl().hash}`,
        query: currentUrl().searchParams.get('q'),
        list: input('list').props.value,
        header: input('header').props.value,
        meetings: elements('list').filter(element => element.props?.meeting).map(element => element.props.meeting.meetingId),
      };
    },
  };
}

test('header submission applies alpha after the list search changes to beta', () => {
  const interaction = searchInteraction();
  interaction.change('list', 'beta');
  interaction.change('header', 'alpha');
  interaction.submitHeader();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?q=alpha', query: 'alpha', list: 'alpha', header: 'alpha', meetings: ['alpha', 'alpha-beta'],
  });
  interaction.change('list', 'beta');
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?q=beta', query: 'beta', list: 'beta', header: 'beta', meetings: ['alpha-beta', 'beta'],
  });
});

test('clearing filters also clears the visible header query before its next submission', () => {
  const interaction = searchInteraction();
  interaction.clear();
  interaction.submitHeader();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings', query: null, list: '', header: '', meetings: ['alpha', 'alpha-beta', 'beta'],
  });
});

test('clearing a status filter after header typing from an absent query stays cleared after scheduled work', () => {
  const interaction = searchInteraction('/meetings?tab=shared#history');
  assert.equal(interaction.snapshot().query, null);
  interaction.selectStatus('done');
  interaction.change('header', 'beta');
  interaction.clear();
  const afterClear = interaction.snapshot();
  interaction.flushTimers();
  const cleared = {
    href: '/meetings?tab=shared#history', query: null, list: '', header: '', meetings: ['alpha', 'alpha-beta', 'beta'],
  };
  assert.deepEqual({ afterClear, afterScheduledWork: interaction.snapshot() }, {
    afterClear: cleared, afterScheduledWork: cleared,
  });
});

test('list query updates preserve the current pathname, other parameters and fragment', () => {
  const interaction = searchInteraction('/accounts/customer?tab=shared&account=a&account=b&q=alpha#history');
  interaction.change('list', 'beta');
  assert.equal(interaction.snapshot().href, '/accounts/customer?tab=shared&account=a&account=b&q=beta#history');
  interaction.clear();
  assert.equal(interaction.snapshot().href, '/accounts/customer?tab=shared&account=a&account=b#history');
  assert.deepEqual(interaction.snapshot().meetings, ['alpha', 'alpha-beta', 'beta']);
});

test('back and forward restore both searches after list edits without extra history entries', () => {
  const interaction = searchInteraction();
  interaction.navigate('/meetings?q=beta');
  interaction.change('list', 'alpha beta');
  interaction.back();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?q=alpha', query: 'alpha', list: 'alpha', header: 'alpha', meetings: ['alpha', 'alpha-beta'],
  });
  interaction.forward();
  const restored = interaction.snapshot();
  assert.equal(restored.query, 'alpha beta');
  assert.equal(restored.list, 'alpha beta');
  assert.equal(restored.header, 'alpha beta');
  assert.deepEqual(restored.meetings, ['alpha-beta']);
});

test('list typing retains spaces and Unicode while header Enter preserves unrelated parameters', () => {
  const interaction = searchInteraction('/meetings?tab=shared&q=alpha');
  for (const value of ['alpha', 'alpha ', 'alpha beta', 'alpha beta 검토']) interaction.change('list', value);
  const typed = interaction.snapshot();
  assert.equal(typed.query, 'alpha beta 검토');
  assert.equal(typed.list, 'alpha beta 검토');
  assert.equal(typed.header, 'alpha beta 검토');
  assert.deepEqual(typed.meetings, ['alpha-beta']);
  interaction.submitHeader();
  assert.equal(new URL(interaction.snapshot().href, 'https://workspace.invalid').searchParams.get('tab'), 'shared');
  assert.deepEqual(interaction.snapshot().meetings, ['alpha-beta']);
});

test('an earlier header edit cannot overwrite a newer list edit or clear after scheduled work', () => {
  const interaction = searchInteraction();
  interaction.change('header', 'alpha beta');
  interaction.change('list', 'beta');
  interaction.flushTimers();
  assert.equal(interaction.snapshot().query, 'beta');
  assert.equal(interaction.snapshot().header, 'beta');
  assert.deepEqual(interaction.snapshot().meetings, ['alpha-beta', 'beta']);
  interaction.change('header', 'alpha');
  interaction.clear();
  interaction.flushTimers();
  assert.equal(interaction.snapshot().query, null);
  assert.equal(interaction.snapshot().header, '');
  assert.deepEqual(interaction.snapshot().meetings, ['alpha', 'alpha-beta', 'beta']);
});

test('header live updates retain trailing spaces and composing Enter does not submit', () => {
  const interaction = searchInteraction();
  interaction.change('header', 'alpha ');
  assert.equal(interaction.snapshot().header, 'alpha ');
  interaction.change('header', 'alpha beta ');
  assert.equal(interaction.snapshot().header, 'alpha beta ');
  assert.equal(interaction.snapshot().list, 'alpha beta');
  assert.equal(interaction.snapshot().query, 'alpha beta');
  assert.deepEqual(interaction.snapshot().meetings, ['alpha-beta']);
  interaction.flushTimers();
  assert.equal(interaction.snapshot().header, 'alpha beta ');
  interaction.change('header', 'beta');
  const beforeComposingEnter = interaction.historyReplacements();
  interaction.submitHeader(true);
  assert.deepEqual(interaction.historyReplacements(), beforeComposingEnter);
  interaction.submitHeader();
  assert.deepEqual(interaction.historyReplacements(), [...beforeComposingEnter, '/meetings?q=beta']);
  assert.equal(interaction.snapshot().query, 'beta');
  assert.deepEqual(interaction.snapshot().meetings, ['alpha-beta', 'beta']);
});

test('returning to the original list query supersedes an older header edit after scheduled work', () => {
  const interaction = searchInteraction();
  interaction.change('header', 'alpha beta');
  interaction.change('list', 'beta');
  interaction.change('list', 'alpha');
  interaction.flushTimers();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?q=alpha', query: 'alpha', list: 'alpha', header: 'alpha', meetings: ['alpha', 'alpha-beta'],
  });
});

test('meetings header edits replace history while preserving repeated parameters and fragment', () => {
  const interaction = searchInteraction();
  interaction.navigate('/meetings?tab=shared&account=a&account=b#history');
  interaction.change('header', ' beta ');
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?tab=shared&account=a&account=b&q=beta#history',
    query: 'beta', list: 'beta', header: ' beta ', meetings: ['alpha-beta', 'beta'],
  });
  interaction.back();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?q=alpha', query: 'alpha', list: 'alpha', header: 'alpha', meetings: ['alpha', 'alpha-beta'],
  });
  interaction.forward();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?tab=shared&account=a&account=b&q=beta#history',
    query: 'beta', list: 'beta', header: 'beta', meetings: ['alpha-beta', 'beta'],
  });
});

test('header search on another page navigates to meetings only on Enter', () => {
  const interaction = searchInteraction('/accounts/customer?tab=meetings#history');
  interaction.change('header', '   ');
  interaction.submitHeader();
  assert.equal(interaction.snapshot().href, '/accounts/customer?tab=meetings#history');
  interaction.change('header', ' beta ');
  interaction.flushTimers();
  interaction.submitHeader(true);
  assert.equal(interaction.snapshot().href, '/accounts/customer?tab=meetings#history');
  interaction.submitHeader();
  assert.deepEqual(interaction.snapshot(), {
    href: '/meetings?q=beta', query: 'beta', list: 'beta', header: ' beta ', meetings: ['alpha-beta', 'beta'],
  });
});

test('actual meeting dates sort both ways with deterministic ID ties and undated rows last', () => {
  const { selectMeetingRows } = loadWorkspace()('@/lib/meetingWorkspace');
  const rows = [
    meeting('invalid-z', 'invalid'), meeting('tie-b'), meeting('older', '2025-10-01T12:00:00Z'),
    meeting('tie-a'), meeting('invalid-a', ''), meeting('newer', '2026-11-08T12:00:00Z'),
  ];
  assert.deepEqual(ids(selectMeetingRows(rows, defaults)), ['newer', 'tie-a', 'tie-b', 'older', 'invalid-a', 'invalid-z']);
  assert.deepEqual(ids(selectMeetingRows(rows, { ...defaults, sort: 'oldest' })), ['older', 'tie-a', 'tie-b', 'newer', 'invalid-a', 'invalid-z']);
});

test('query, statuses and UTC boundaries intersect while undated matches stay visible', () => {
  const { selectMeetingRows } = loadWorkspace()('@/lib/meetingWorkspace');
  const rows = [
    meeting('match', '2026-11-07T00:00:00Z', { summary: 'Cloud review' }),
    meeting('end', '2026-11-07T23:59:59.999Z', { tags: ['CLOUD'] }),
    meeting('undated', 'invalid', { title: 'Cloud unknown date' }),
    meeting('wrong-status', undefined, { title: 'Cloud', status: 'error' }),
    meeting('wrong-query'),
    meeting('too-early', '2026-11-06T23:59:59.999Z', { title: 'Cloud' }),
    meeting('too-late', '2026-11-08T00:00:00Z', { title: 'Cloud' }),
  ];
  assert.deepEqual(ids(selectMeetingRows(rows, {
    ...defaults, query: '  cLoUd  ', statuses: ['done'],
    from: '2026-11-07T00:00:00Z', to: '2026-11-07T23:59:59.999Z',
  })), ['end', 'match', 'undated']);
  assert.deepEqual(ids(selectMeetingRows(rows, { ...defaults, statuses: ['error', 'recording'] })), ['wrong-status']);
});

test('deduplication keeps the latest supplied record and does not mutate inputs', () => {
  const { selectMeetingRows, groupMeetingRows } = loadWorkspace()('@/lib/meetingWorkspace');
  const rows = Object.freeze([
    Object.freeze(meeting('same', undefined, { title: 'old' })),
    Object.freeze(meeting('other', undefined, { accountId: 'customer' })),
    Object.freeze(meeting('same', undefined, { title: 'updated' })),
  ]);
  const snapshot = JSON.stringify(rows);
  const result = selectMeetingRows(rows, defaults);
  assert.deepEqual(ids(result), ['other', 'same']);
  assert.equal(result[1].title, 'updated');
  groupMeetingRows(result);
  assert.equal(JSON.stringify(rows), snapshot);
});

test('account groups preserve selected order and keep unassigned meetings in their own group', () => {
  const { groupMeetingRows } = loadWorkspace()('@/lib/meetingWorkspace');
  const groups = groupMeetingRows([
    meeting('alpha-new', undefined, { accountId: 'alpha' }),
    meeting('unassigned'), meeting('beta', undefined, { accountId: 'beta' }),
    meeting('alpha-old', undefined, { accountId: 'alpha' }),
    meeting('empty-account', undefined, { accountId: '' }),
  ]);
  assert.deepEqual(Array.from(groups, group => [group.accountId, ids(group.meetings)]), [
    ['alpha', ['alpha-new', 'alpha-old']], [null, ['unassigned', 'empty-account']], ['beta', ['beta']],
  ]);
});

test('internal meetings group after accounts in fixed category order and never override an account link', () => {
  const { groupMeetingRows } = loadWorkspace()('@/lib/meetingWorkspace');
  const groups = groupMeetingRows([
    meeting('other', undefined, { meetingCategory: 'internal_other' }),
    meeting('customer', undefined, { accountId: 'alpha' }),
    meeting('one-on-one', undefined, { meetingCategory: 'internal_1on1' }),
    meeting('team', undefined, { meetingCategory: 'internal_team' }),
    meeting('linked-wins', undefined, { accountId: 'alpha', meetingCategory: 'internal_team' }),
    meeting('unknown', undefined, { meetingCategory: 'internal_bogus' }),
  ]);
  assert.deepEqual(Array.from(groups, group => [group.accountId, group.category, ids(group.meetings)]), [
    ['alpha', null, ['customer', 'linked-wins']], [null, null, ['unknown']],
    [null, 'internal_team', ['team']], [null, 'internal_1on1', ['one-on-one']], [null, 'internal_other', ['other']],
  ]);
});

test('category filter separates customer, internal and each internal subtype', () => {
  const { matchesCategoryFilter } = loadWorkspace()('@/lib/meetingCategory');
  const rows = [
    meeting('customer', undefined, { accountId: 'alpha' }),
    meeting('unassigned'),
    meeting('team', undefined, { meetingCategory: 'internal_team' }),
    meeting('one-on-one', undefined, { meetingCategory: 'internal_1on1' }),
  ];
  const pick = filter => ids(rows.filter(row => matchesCategoryFilter(row, filter)));
  assert.deepEqual(pick(''), ['customer', 'unassigned', 'team', 'one-on-one']);
  assert.deepEqual(pick('customer'), ['customer']);
  assert.deepEqual(pick('internal'), ['team', 'one-on-one']);
  assert.deepEqual(pick('internal_1on1'), ['one-on-one']);
});

test('previews inspect at most 2048 code points and emit at most 180 without splitting Unicode', () => {
  const { meetingPreview } = loadWorkspace()('@/lib/meetingWorkspace');
  assert.equal(meetingPreview(' '.repeat(2048) + 'must not be examined'), '');
  assert.equal(meetingPreview('🪐'.repeat(2048) + 'must not be examined').includes('must'), false);
  const preview = meetingPreview('🪐'.repeat(50000));
  assert.ok(Array.from(preview).length <= 180);
  assert.ok(preview.length > 0);
  assert.equal(preview.isWellFormed(), true);
  assert.equal(meetingPreview(''), '');
  assert.equal(meetingPreview('## Review\n\n**Next** [steps](https://example.invalid)\n- Ship'), 'Review Next steps Ship');
});

test('local date input boundaries use the viewer timezone including DST transitions', () => {
  const { meetingDateBoundary } = loadWorkspace()('@/lib/meetingWorkspace');
  const originalTimezone = process.env.TZ;
  try {
    process.env.TZ = 'America/New_York';
    assert.equal(meetingDateBoundary('2026-11-01', 'from'), '2026-11-01T04:00:00.000Z');
    assert.equal(meetingDateBoundary('2026-11-01', 'to'), '2026-11-02T04:59:59.999Z');
    process.env.TZ = 'Asia/Seoul';
    assert.equal(meetingDateBoundary('2026-11-07', 'from'), '2026-11-06T15:00:00.000Z');
    assert.equal(meetingDateBoundary('', 'to'), undefined);
  } finally {
    if (originalTimezone === undefined) delete process.env.TZ;
    else process.env.TZ = originalTimezone;
  }
});

test('rows render previews as text, keep one detail link, and restrict shared deletion', () => {
  const load = loadWorkspace();
  const { MeetingRow } = load('@/components/workspace/MeetingRow');
  const preview = '<img src=x onerror=alert(1)><script>alert(1)</script> & notes';
  const renderRow = extra => renderToStaticMarkup(React.createElement(MeetingRow, {
    meeting: meeting('safe', 'invalid', extra), accountName: '고객', preview,
    onDelete() {},
  }));
  const own = renderRow({});
  assert.doesNotMatch(own, /<(script|img)\b|dangerouslySetInnerHTML/);
  assert.match(own, /&lt;img/);
  assert.equal((own.match(/href="\/meeting\/safe"/g) || []).length, 1);
  assert.match(own, /날짜 미정/);
  assert.match(own, /미팅 삭제/);
  const shared = renderRow({ isShared: true, ownerUserId: 'someone-else' });
  assert.doesNotMatch(shared, /미팅 삭제/);
  assert.match(shared, /공유/);
});

test('loaded-result scope includes deduplicated count and remains visible on empty matches', () => {
  const rows = [meeting('first'), meeting('second'), meeting('first')];
  const html = renderList({ meetings: rows, hasMore: true });
  assert.match(html, /불러온 2개 미팅/);
  assert.match(html, /검색·정렬·상태·기간 필터는 불러온 결과에만 적용/);
  const empty = renderList({ meetings: rows, hasMore: true }, 'q=unmatched');
  assert.match(empty, /불러온 2개 미팅/);
  assert.match(empty, /Load More/);
  assert.doesNotMatch(empty, /아직 미팅이 없습니다/);
});

test('mounts at most 100 unique rows and separates window navigation from server paging', () => {
  const rows = Array.from({ length: 200 }, (_, index) => meeting(`row-${String(index).padStart(3, '0')}`));
  const html = renderList({ meetings: rows, hasMore: true });
  assert.equal((html.match(/href="\/meeting\//g) || []).length, 100);
  assert.match(html, /불러온 200개 미팅/);
  assert.match(html, /이전 100개/);
  assert.match(html, /다음 100개/);
  assert.match(html, /Load More/);
});

test('changing and clearing a filter resets the window instead of restoring an old page', () => {
  const states = [];
  let stateIndex = 0;
  let changedDuringRender = false;
  const { MeetingList } = loadWorkspace('', {
    useState(initial) {
      const slot = stateIndex++;
      if (!(slot in states)) states[slot] = typeof initial === 'function' ? initial() : initial;
      return [states[slot], value => {
        states[slot] = typeof value === 'function' ? value(states[slot]) : value;
        changedDuringRender = true;
      }];
    },
    useMemo: callback => callback(),
  })('@/components/MeetingList');
  const rows = Array.from({ length: 200 }, (_, index) => meeting(`row-${String(index).padStart(3, '0')}`));
  const render = () => {
    let tree;
    do {
      stateIndex = 0;
      changedDuringRender = false;
      tree = MeetingList({ ...listProps, meetings: rows });
    } while (changedDuringRender);
    const elements = [];
    const visit = element => {
      if (!element || typeof element !== 'object') return;
      if (Array.isArray(element)) return element.forEach(visit);
      elements.push(element);
      visit(element.props?.children);
    };
    visit(tree);
    return elements;
  };
  const firstMeeting = elements => elements.find(element => element.props?.meeting)?.props.meeting.meetingId;
  let elements = render();
  elements.find(element => element.type === 'button' && element.props.children === '다음 100개').props.onClick();
  elements = render();
  assert.equal(firstMeeting(elements), 'row-100');
  elements.find(element => element.props?.['aria-label'] === '미팅 검색').props.onChange({ target: { value: 'row-001' } });
  elements = render();
  assert.equal(firstMeeting(elements), 'row-001');
  elements.find(element => element.props?.['aria-label'] === '미팅 검색').props.onChange({ target: { value: '' } });
  assert.equal(firstMeeting(render()), 'row-000');
});

test('loading and retry states preserve filters, loaded rows, and recording navigation', () => {
  const html = renderList({ meetings: [meeting('retained')], error: '추가 미팅 실패', hasMore: true });
  assert.match(html, /추가 미팅 실패/);
  assert.match(html, /href="\/meeting\/retained"/);
  assert.match(html, /href="\/record"/);
  const refreshing = renderList({ meetings: [meeting('retained')], isLoading: true });
  assert.match(refreshing, /href="\/meeting\/retained"/);
  assert.match(refreshing, /새로고침/);
  const initial = renderList({ isLoading: true });
  assert.match(initial, /aria-label="미팅 정렬"/);
  assert.match(initial, /aria-label="미팅 검색"/);
  assert.doesNotMatch(initial, /아직 미팅이 없습니다/);
});

'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { CONTACT_FIELDS, contactFromRow, isContactAccessDenied, mapContactHeaders, type ContactField, type ContactMapping } from '@/lib/contactImport';
import { readContactFile } from '@/lib/contactFile';
import { defaultDecision, groupByCompany, runBulkImport, type BulkRowResult, type CompanyGroup, type FieldConflict, type GroupDecision, type MatchAccount } from '@/lib/contactBulkImport';
import type { ContactInput } from '@/types/meeting';

const fieldLabel = Object.fromEntries(CONTACT_FIELDS) as Record<ContactField, string>;
const statusLabel: Record<BulkRowResult['status'], string> = { created: '추가', filled: '빈 칸 보강', unchanged: '변경 없음', dropped: '드랍', invalid: '이름 없음', failed: '실패' };

/** Loads the accounts the user directly belongs to, with names, aliases and domains for matching. */
async function loadMatchAccounts(userId: string, signal: AbortSignal): Promise<MatchAccount[]> {
  const { accounts } = await accountApi.list({ scope: 'direct', expectedUserId: userId });
  const direct = accounts.filter((a) => !a.inheritedFromAccountId);
  const out: MatchAccount[] = [];
  let next = 0;
  const worker = async () => {
    while (next < direct.length && !signal.aborted) {
      const summary = direct[next++];
      try {
        const detail = await accountApi.get(summary.accountId, { signal, expectedUserId: userId });
        out.push({ accountId: summary.accountId, name: detail.name || summary.name, aliases: detail.aliases || [], domains: detail.domains || [] });
      } catch (error) {
        if (signal.aborted) throw error;
        out.push({ accountId: summary.accountId, name: summary.name, aliases: [], domains: [] });
      }
    }
  };
  await Promise.all(Array.from({ length: 6 }, worker));
  return out.sort((a, b) => a.name.localeCompare(b.name, 'ko'));
}

interface ConflictRow { row: number; accountId: string; contactId: string; name: string; conflict: FieldConflict }

/** Settings-wide Remember/CSV/Excel import that routes each company to its account. */
export function BulkContactImport({ userId }: { userId: string }) {
  const [accounts, setAccounts] = useState<MatchAccount[] | null>(null);
  const [table, setTable] = useState<string[][]>([]);
  const [mapping, setMapping] = useState<ContactMapping>({});
  const [decisions, setDecisions] = useState<Record<string, GroupDecision>>({});
  const [results, setResults] = useState<Record<number, BulkRowResult>>({});
  const [applied, setApplied] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef<AbortController | null>(null);
  const requestIds = useRef(new Map<string, string>());
  const fileGeneration = useRef(0);

  useEffect(() => {
    const current = new AbortController();
    controller.current = current;
    return () => { current.abort(); };
  }, []);

  const contacts = useMemo<ContactInput[]>(() => table.slice(1).map((row) => contactFromRow(row, mapping)), [table, mapping]);
  const groups = useMemo<CompanyGroup[]>(() => accounts ? groupByCompany(contacts, accounts) : [], [contacts, accounts]);
  const accountName = (id: string) => accounts?.find((a) => a.accountId === id)?.name || id;
  const decisionOf = (group: CompanyGroup) => decisions[group.key] ?? defaultDecision(group);

  const chooseFile = async (file?: File) => {
    if (!file) return;
    const active = controller.current;
    const generation = ++fileGeneration.current;
    const current = () => !!active && !active.signal.aborted && generation === fileGeneration.current;
    setError(''); setTable([]); setResults({}); setDecisions({}); setApplied(new Set());
    try {
      const [rows, loaded] = await Promise.all([readContactFile(file), accounts ? Promise.resolve(accounts) : loadMatchAccounts(userId, active!.signal)]);
      if (!current()) return;
      setAccounts(loaded);
      setTable(rows);
      setMapping(mapContactHeaders(rows[0]));
    } catch (err) {
      if (!current()) return;
      setError(isContactAccessDenied(err) ? '고객사 목록을 불러올 권한이 없습니다.' : err instanceof Error ? err.message : '파일을 읽지 못했습니다.');
    }
  };

  const setDecision = (key: string, decision: GroupDecision) => setDecisions((old) => ({ ...old, [key]: decision }));

  const submit = async () => {
    const active = controller.current;
    if (!active || active.signal.aborted || busy || mapping.name === undefined) return;
    setBusy(true); setError('');
    const signal = active.signal;
    // Rows already saved in this file are not sent again on a retry.
    const skipRows = new Set(Object.entries(results).filter(([, r]) => r.status === 'created' || r.status === 'filled' || r.status === 'unchanged').map(([row]) => Number(row)));
    try {
      await runBulkImport(contacts, groups, decisions, {
        createAccount: async (name, domains) => {
          const key = `${name}\n${domains.join(',')}`;
          let requestId = requestIds.current.get(key);
          if (!requestId) { requestId = crypto.randomUUID(); requestIds.current.set(key, requestId); }
          const account = await accountApi.create({ name, domains: domains.length ? domains : undefined, requestId }, userId);
          const created: MatchAccount = { accountId: account.accountId, name: account.name, aliases: account.aliases || [], domains: account.domains || [] };
          if (!signal.aborted) setAccounts((old) => [...(old || []).filter((a) => a.accountId !== created.accountId), created].sort((a, b) => a.name.localeCompare(b.name, 'ko')));
          return account.accountId;
        },
        listContacts: (accountId, cursor) => accountApi.contacts(accountId, userId, cursor, signal),
        createContact: (accountId, contact) => accountApi.createContact(accountId, contact, userId, signal),
        updateContact: (accountId, contactId, contact) => accountApi.updateContact(accountId, contactId, contact, userId, signal),
      }, {
        signal,
        skipRows,
        // The group now targets the created account, so a retry never creates it twice.
        onAccountCreated: (groupKey, accountId) => { if (!signal.aborted) setDecision(groupKey, { action: 'account', accountId }); },
        onProgress: (row, result) => { if (!signal.aborted) setResults((old) => ({ ...old, [row]: result })); },
      });
    } catch (err) {
      if (signal.aborted) return;
      setError(isContactAccessDenied(err)
        ? '일부 고객사에 연락처를 추가할 권한이 없습니다. 고객사 멤버인지 확인해 주세요.'
        : '가져오기가 중단되었습니다. 표시된 결과를 확인하고 다시 시도해 주세요. 이미 저장된 행은 다시 보내지 않습니다.');
    } finally { if (!signal.aborted) setBusy(false); }
  };

  const conflicts: ConflictRow[] = Object.entries(results).flatMap(([row, result]) =>
    (result.conflicts || []).map((conflict) => ({ row: Number(row), accountId: result.accountId!, contactId: result.contactId!, name: contacts[Number(row)]?.name || '', conflict })));
  const conflictKey = (c: ConflictRow) => `${c.row}:${c.conflict.field}`;

  const applyConflict = async (item: ConflictRow) => {
    const active = controller.current;
    if (!active || busy) return;
    setBusy(true); setError('');
    try {
      // Re-read so the update carries the current revision and other fields.
      let cursor = '';
      let current;
      do {
        const page = await accountApi.contacts(item.accountId, userId, cursor, active.signal);
        current = page.contacts.find((c) => c.contactId === item.contactId);
        cursor = current ? '' : page.nextCursor || '';
      } while (cursor);
      if (!current) throw new Error('연락처를 찾지 못했습니다.');
      // Apply only what the user saw: a value changed since then is not overwritten.
      if ((current[item.conflict.field] || '').trim() !== item.conflict.current) {
        if (!active.signal.aborted) setError(`${item.name}의 ${fieldLabel[item.conflict.field]} 값이 그사이 바뀌어 적용하지 않았습니다. 고객사 연락처에서 확인해 주세요.`);
        return;
      }
      const next: ContactInput & { revision: string } = { name: current.name, revision: current.revision };
      for (const [field] of CONTACT_FIELDS) if (current[field]) next[field] = current[field];
      next[item.conflict.field] = item.conflict.incoming;
      await accountApi.updateContact(item.accountId, item.contactId, next, userId, active.signal);
      if (!active.signal.aborted) setApplied((old) => new Set(old).add(conflictKey(item)));
    } catch (err) {
      if (!active.signal.aborted) setError(isContactAccessDenied(err) ? '이 고객사의 연락처를 수정할 권한이 없습니다.' : '변경을 적용하지 못했습니다. 다시 시도해 주세요.');
    } finally { if (!active.signal.aborted) setBusy(false); }
  };

  const counts = Object.values(results).reduce<Record<string, number>>((acc, r) => { acc[r.status] = (acc[r.status] || 0) + 1; return acc; }, {});
  const pending = groups.filter((g) => g.match.kind !== 'exact');
  const exact = groups.filter((g) => g.match.kind === 'exact');
  const importing = groups.reduce((n, g) => n + (decisionOf(g).action === 'drop' ? 0 : g.rows.length), 0);
  const headers = table[0] || [];
  const nameless = contacts.filter((c) => !c.name.trim()).length;
  const done = Object.keys(results).length > 0 && !busy;

  const groupTitle = (g: CompanyGroup) => g.company || (g.domain ? `@${g.domain}` : '회사 정보 없음');
  const decisionControl = (g: CompanyGroup) => {
    const decision = decisionOf(g);
    const value = decision.action === 'account' ? `a:${decision.accountId}` : decision.action;
    const candidates = g.match.kind === 'similar' ? g.match.candidates : [];
    return <div className="mt-2 flex flex-wrap items-center gap-2">
      <select aria-label={`${groupTitle(g)} 처리 방법`} value={value} disabled={busy} onChange={(e) => {
        const v = e.target.value;
        if (v === 'drop') setDecision(g.key, { action: 'drop' });
        else if (v === 'new') setDecision(g.key, { action: 'new', name: g.company || g.domain.split('.')[0] || '' });
        else setDecision(g.key, { action: 'account', accountId: v.slice(2) });
      }} className="rounded border border-slate-200 bg-white p-1.5 text-sm dark:border-white/10 dark:bg-slate-900">
        <option value="drop">드랍 (가져오지 않음)</option>
        <option value="new">새 고객사로 추가</option>
        {candidates.length > 0 && <optgroup label="비슷한 고객사">{candidates.map((id) => <option key={id} value={`a:${id}`}>{accountName(id)}</option>)}</optgroup>}
        <optgroup label="다른 고객사 선택">{(accounts || []).filter((a) => !candidates.includes(a.accountId)).map((a) => <option key={a.accountId} value={`a:${a.accountId}`}>{a.name}</option>)}</optgroup>
      </select>
      {decision.action === 'new' && <input aria-label={`${groupTitle(g)} 새 고객사 이름`} value={decision.name} disabled={busy} maxLength={200}
        onChange={(e) => setDecision(g.key, { action: 'new', name: e.target.value })}
        className="min-w-48 rounded border border-slate-200 bg-white p-1.5 text-sm dark:border-white/10 dark:bg-slate-900" placeholder="고객사 이름" />}
      {decision.action === 'new' && g.domain && <span className="text-xs text-slate-500">도메인 @{g.domain}도 등록합니다</span>}
    </div>;
  };

  const people = (g: CompanyGroup) => <details className="mt-1 text-xs text-slate-500">
    <summary className="cursor-pointer">{g.rows.length}명 보기</summary>
    <ul className="mt-1 space-y-0.5">{g.rows.map((row) => { const c = contacts[row]; const r = results[row]; return <li key={row}>
      {c.name}{c.title ? ` · ${c.title}` : ''}{c.email ? ` · ${c.email}` : ''}{r && <span className="ml-2 font-semibold">{statusLabel[r.status]}</span>}
    </li>; })}</ul>
  </details>;

  return <div className="space-y-4">
    <p className="text-sm text-slate-600 dark:text-text-muted">Remember에서 내보낸 CSV 또는 .xlsx 파일을 올리면 회사명과 이메일 도메인으로 고객사를 찾아 연락처를 넣습니다. 같은 사람(이메일·휴대폰·근무처 전화 일치)은 비어 있는 칸만 채우고, 다른 값은 덮어쓰지 않고 변경 후보로 보여 드립니다. 내가 직접 멤버인 고객사에만 넣을 수 있습니다.</p>
    <input type="file" accept=".csv,.xlsx" disabled={busy} className="text-sm" onChange={(e) => { void chooseFile(e.target.files?.[0]); e.target.value = ''; }} />
    {error && <p role="alert" className="text-sm text-red-600">{error}</p>}
    {table.length > 0 && accounts && <>
      <details className="rounded border border-slate-200 p-3 dark:border-white/10">
        <summary className="cursor-pointer text-sm font-semibold">열 연결 확인</summary>
        <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {CONTACT_FIELDS.map(([field, label]) => <label key={field} className="text-xs text-slate-600 dark:text-slate-300">{label}{field === 'name' && ' *'}
            <select value={mapping[field] ?? ''} disabled={busy || Object.keys(results).length > 0} onChange={(e) => setMapping((old) => {
              const next = { ...old };
              if (e.target.value === '') delete next[field]; else next[field] = Number(e.target.value);
              return next;
            })} className="mt-1 w-full rounded border border-slate-200 bg-white p-1.5 dark:border-white/10 dark:bg-slate-900">
              <option value="">가져오지 않음</option>{headers.map((header, i) => <option key={i} value={i}>{header}</option>)}
            </select>
          </label>)}
        </div>
      </details>
      {mapping.name === undefined && <p className="text-sm text-amber-700">이름 열을 연결해 주세요.</p>}
      {mapping.name !== undefined && nameless > 0 && <p className="text-xs text-amber-700">이름이 비어 있는 {nameless}행은 가져오지 않습니다.</p>}

      {pending.length > 0 && <section>
        <h4 className="text-sm font-semibold">확인이 필요한 회사 {pending.length}곳</h4>
        <p className="text-xs text-slate-500">고르지 않은 회사는 드랍됩니다.</p>
        <ul className="mt-2 space-y-2">{pending.map((g) => <li key={g.key} className="rounded border border-amber-200 bg-amber-50/50 p-3 dark:border-amber-500/30 dark:bg-amber-500/5">
          <div className="text-sm font-semibold">{groupTitle(g)} <span className="font-normal text-slate-500">· {g.match.kind === 'similar' ? '비슷한 고객사가 있습니다' : '일치하는 고객사가 없습니다'}</span></div>
          {decisionControl(g)}
          {people(g)}
        </li>)}</ul>
      </section>}

      {exact.length > 0 && <details className="rounded border border-slate-200 p-3 dark:border-white/10">
        <summary className="cursor-pointer text-sm font-semibold">자동으로 연결된 회사 {exact.length}곳 · {exact.reduce((n, g) => n + g.rows.length, 0)}명</summary>
        <ul className="mt-2 space-y-2">{exact.map((g) => <li key={g.key} className="text-sm">
          <span className="font-semibold">{groupTitle(g)}</span> → {g.match.kind === 'exact' && accountName(g.match.accountId)}
          {decisionControl(g)}
          {people(g)}
        </li>)}</ul>
      </details>}

      <button type="button" disabled={busy || mapping.name === undefined || importing === 0} onClick={() => void submit()}
        className="rounded bg-primary px-3 py-2 text-sm font-semibold text-white disabled:opacity-40">{busy ? '가져오는 중…' : `${importing}명 가져오기`}</button>

      {done && <p role="status" className="text-sm">결과: 추가 {counts.created || 0} · 빈 칸 보강 {counts.filled || 0} · 변경 없음 {counts.unchanged || 0} · 드랍 {counts.dropped || 0}{counts.failed ? ` · 실패 ${counts.failed}` : ''}{counts.invalid ? ` · 이름 없음 ${counts.invalid}` : ''}</p>}
      {done && counts.failed > 0 && <p className="text-xs text-slate-500">다시 가져오기를 누르면 아직 저장되지 않은 행만 다시 처리합니다.</p>}

      {conflicts.length > 0 && <section>
        <h4 className="text-sm font-semibold">변경 후보 {conflicts.length}건</h4>
        <p className="text-xs text-slate-500">이미 값이 있어 바꾸지 않았습니다. 새 값으로 바꾸려면 적용을 누르세요.</p>
        <ul className="mt-2 space-y-1 text-sm">{conflicts.map((c) => <li key={conflictKey(c)} className="flex flex-wrap items-center gap-2">
          <span className="font-semibold">{c.name}</span><span className="text-slate-500">({accountName(c.accountId)}) {fieldLabel[c.conflict.field]}:</span>
          <span className="line-through text-slate-400">{c.conflict.current}</span><span>→ {c.conflict.incoming}</span>
          {applied.has(conflictKey(c)) ? <span className="text-xs text-emerald-600">적용됨</span>
            : <button type="button" disabled={busy} onClick={() => void applyConflict(c)} className="text-xs font-semibold text-primary disabled:opacity-40">적용</button>}
        </li>)}</ul>
      </section>}
    </>}
  </div>;
}

'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { CONTACT_FIELDS, PEOPLE_RESOLVE_FAILED, PEOPLE_RESOLVE_RETRY, contactFromRow, importContacts, isContactAccessDenied, mapContactHeaders, requestPeopleResolve, type ContactField, type ContactMapping, type ImportStatus } from '@/lib/contactImport';
import { readContactFile } from '@/lib/contactFile';
import { useContactFileDrop } from '@/lib/contactFileDrop';
import { planAccountRows, type RowAccountMatch } from '@/lib/contactBulkImport';

const matchLabel: Record<RowAccountMatch, string> = { exact: '일치', contained: '포함', none: '—' };

/** The account rows are matched against: its name, aliases and email domains. */
export interface ImportTargetAccount { name: string; aliases: string[]; domains: string[] }

const statusLabel: Record<ImportStatus, string> = { created: '추가됨', duplicate: '중복으로 건너뜀', failed: '실패 또는 저장 여부 확인 필요', invalid: '이름 없음' };

function planFor(table: string[][], mapping: ContactMapping, account: ImportTargetAccount) {
  const columns = { company: mapping.company, email: mapping.email };
  return planAccountRows(table.slice(1).map((row) => contactFromRow(row, columns)), account,
    mapping.company !== undefined || mapping.email !== undefined);
}

export function ContactImport({ accountId, userId, account, onImported, onDenied }: { accountId: string; userId: string; account: ImportTargetAccount; onImported: () => void; onDenied: () => void }) {
  const [table, setTable] = useState<string[][]>([]);
  const [mapping, setMapping] = useState<ContactMapping>({});
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [statuses, setStatuses] = useState<Record<number, ImportStatus>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [resolveFailed, setResolveFailed] = useState(false);
  const controller = useRef<AbortController | null>(null);
  // Matching reads only the company and email columns, so remapping other columns keeps the selection.
  const companyColumn = mapping.company; const emailColumn = mapping.email;
  const accountKey = JSON.stringify([account.name, account.aliases, account.domains]);
  const plan = useMemo(() => {
    const [name, aliases, domains] = JSON.parse(accountKey) as [string, string[], string[]];
    return planFor(table, { company: companyColumn, email: emailColumn }, { name, aliases, domains });
  }, [table, companyColumn, emailColumn, accountKey]);
  const fileGeneration = useRef(0);
  useEffect(() => {
    const current = new AbortController();
    controller.current = current;
    return () => { current.abort(); };
  }, []);

  const chooseFile = async (file?: File) => {
    if (!file) return;
    const active = controller.current;
    const generation = ++fileGeneration.current;
    const current = () => !!active && !active.signal.aborted && generation === fileGeneration.current;
    setError(''); setTable([]); setStatuses({});
    try {
      const rows = await readContactFile(file);
      if (!current()) return;
      const headers = rows[0];
      const nextTable = [headers, ...rows.slice(1)];
      const nextMapping = mapContactHeaders(headers);
      setTable(nextTable);
      setMapping(nextMapping);
      setSelected(new Set(planFor(nextTable, nextMapping, account).selected));
    } catch (err) {
      if (current()) setError(err instanceof Error ? err.message : '파일을 읽지 못했습니다.');
    }
  };

  const fileDrop = useContactFileDrop({ disabled: busy, onFile: (file) => { void chooseFile(file); }, onReject: setError });


  const submit = async () => {
    const active = controller.current;
    if (!active || active.signal.aborted || busy || mapping.name === undefined) return;
    const indexes = [...selected].filter((i) => statuses[i] !== 'created');
    if (!indexes.length) return;
    setBusy(true); setError(''); setResolveFailed(false);
    // Rows defer their people resolve requests; one request follows the run,
    // also when every row was already saved (an earlier request may have failed).
    let wrote = false;
    let denied = false;
    try {
      const done = await importContacts(indexes.map((i) => contactFromRow(table[i + 1], mapping)),
        (cursor) => accountApi.contacts(accountId, userId, cursor, active.signal),
        (contact) => { wrote = true; return accountApi.createContact(accountId, contact, userId, active.signal, true); },
        (index, status) => { if (!active.signal.aborted) setStatuses((old) => ({ ...old, [indexes[index]]: status })); });
      wrote = wrote || done.some((status) => status === 'created' || status === 'duplicate');
      if (!active.signal.aborted) onImported();
    } catch (err) {
      if (!active.signal.aborted) {
        if (isContactAccessDenied(err)) { denied = true; onDenied(); return; }
        setError('가져오기가 중단되었습니다. 표시된 결과를 확인하고 다시 시도해 주세요. 저장 여부가 불확실한 항목은 연락처 목록을 확인해 주세요.');
        onImported();
      }
    } finally {
      if (wrote && !denied && !(await requestPeopleResolve((s) => accountApi.requestPeopleResolve(accountId, userId, s), active.signal)) && !active.signal.aborted) setResolveFailed(true);
      if (!active.signal.aborted) setBusy(false);
    }
  };

  const retryResolve = async () => {
    const active = controller.current;
    if (!active || busy) return;
    setBusy(true);
    const ok = await requestPeopleResolve((s) => accountApi.requestPeopleResolve(accountId, userId, s), active.signal);
    if (!active.signal.aborted) { setResolveFailed(!ok); setBusy(false); }
  };

  const headers = table[0] || [];
  const unmapped = headers.filter((_, i) => !Object.values(mapping).includes(i));
  return <details className="mt-4 border-t border-slate-200 dark:border-white/10 pt-4">
    <summary className="cursor-pointer text-sm font-semibold">Remember / CSV / Excel 가져오기</summary>
    <p className="mt-2 text-xs text-slate-500">현재 고객에게 추가합니다. CSV 또는 .xlsx 파일을 선택하고 대상 행과 열을 확인하세요. 파일은 5MB, Excel은 압축 해제 후 16MB까지 지원합니다. Excel에서 전화번호는 텍스트 형식이어야 앞자리 0이 보존됩니다.</p>
    <div {...fileDrop.zoneProps} data-contact-drop-zone className={`mt-2 max-w-md rounded-xl border-2 border-dashed p-3 transition-colors ${fileDrop.dragging ? 'border-primary bg-primary/5' : 'border-slate-200 dark:border-white/10'}`}>
      <input type="file" accept=".csv,.xlsx" disabled={busy} className="block w-full max-w-md cursor-pointer text-sm text-slate-600 dark:text-text-muted file:mr-3 file:cursor-pointer file:rounded-lg file:border file:border-slate-300 file:bg-white file:px-3 file:py-1.5 file:text-sm file:font-semibold file:text-slate-700 hover:file:bg-slate-50 dark:file:border-white/10 dark:file:bg-white/5 dark:file:text-text-main dark:hover:file:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30" onChange={(e) => { void chooseFile(e.target.files?.[0]); e.target.value = ''; }} />
      <p className="mt-2 text-xs text-slate-500 dark:text-text-muted">Remember에서 내보낸 파일을 여기로 끌어다 놓아도 됩니다.</p>
    </div>
    {error && <p role="alert" className="mt-2 text-sm text-red-600">{error}</p>}
    {resolveFailed && <p role="alert" className="mt-2 text-sm text-amber-700">{PEOPLE_RESOLVE_FAILED}{' '}
      <button type="button" disabled={busy} className="underline" onClick={() => { void retryResolve(); }}>{PEOPLE_RESOLVE_RETRY}</button></p>}
    {table.length > 0 && <>
      <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {CONTACT_FIELDS.map(([field, label]) => <label key={field} className="text-xs text-slate-600 dark:text-slate-300">{label}{field === 'name' && ' *'}
          <select value={mapping[field] ?? ''} disabled={busy} onChange={(e) => {
            const next = { ...mapping };
            if (e.target.value === '') delete next[field as ContactField]; else next[field as ContactField] = Number(e.target.value);
            setMapping(next);
            // A new company or email column redoes the automatic selection; rows already added stay unselected.
            if (field === 'company' || field === 'email') {
              setSelected(new Set([...planFor(table, next, account).selected].filter((i) => statuses[i] !== 'created')));
            }
          }} className="mt-1 w-full rounded border border-slate-200 bg-white p-1.5 dark:border-white/10 dark:bg-slate-900">
            <option value="">가져오지 않음</option>{headers.map((header, i) => <option key={i} value={i}>{header}</option>)}
          </select>
        </label>)}
      </div>
      {unmapped.length > 0 && <p className="mt-2 text-xs text-amber-700">연결되지 않은 열: {unmapped.join(', ')}</p>}
      <p className="mt-3 text-xs text-slate-600 dark:text-slate-300">{plan.matches.some((match) => match !== null)
        ? `회사명이 이 고객과 일치하거나 대부분 포함되는 ${plan.selected.size}명을 자동 선택했습니다 (전체 ${table.length - 1}명). 다른 행은 직접 선택하세요.`
        : '회사 또는 이메일 열이 없어 전체 행을 선택했습니다.'}</p>
      <p className="mt-1 text-xs text-slate-500">이메일 또는 휴대폰·근무처 전화가 같으면 건너뜁니다. 대표번호를 공유하는 사람도 중복으로 표시될 수 있습니다. 동시 가져오기는 중복이 생길 수 있습니다.</p>
      <div className="mt-2 max-h-80 overflow-auto rounded border border-slate-200 dark:border-white/10">
        <table className="w-full text-left text-xs"><thead><tr><th className="p-2"><input aria-label="전체 행 선택" type="checkbox" checked={selected.size === table.length - 1} disabled={busy} onChange={(e) => setSelected(e.target.checked ? new Set(table.slice(1).map((_, i) => i)) : new Set())} /></th><th>이름</th><th>회사</th><th>매칭</th><th>이메일 / 전화</th><th>결과</th></tr></thead>
          <tbody>{plan.order.map((i) => { const contact = contactFromRow(table[i + 1], mapping); const match = plan.matches[i]; return <tr key={i} className="border-t border-slate-100 dark:border-white/5"><td className="p-2"><input aria-label={`${i + 2}행 선택`} type="checkbox" checked={selected.has(i)} disabled={busy || statuses[i] === 'created'} onChange={(e) => setSelected((old) => { const next = new Set(old); if (e.target.checked) next.add(i); else next.delete(i); return next; })} /></td><td className="p-2">{contact.name || '—'}<details><summary className="cursor-pointer text-primary">미리보기</summary><div className="whitespace-pre-wrap">{CONTACT_FIELDS.filter(([field]) => contact[field]).map(([field, label]) => <div key={field}>{label}: {contact[field]}</div>)}</div></details></td><td className="p-2">{contact.company || '—'}</td><td className="p-2">{match ? matchLabel[match] : '—'}</td><td className="p-2">{contact.email || contact.phone || contact.workPhone || '—'}</td><td className="p-2">{statuses[i] ? statusLabel[statuses[i]] : '대기'}</td></tr>; })}</tbody>
        </table>
      </div>
      <button type="button" disabled={busy || mapping.name === undefined || selected.size === 0} onClick={() => void submit()} className="mt-3 rounded bg-primary px-3 py-2 text-sm font-semibold text-white disabled:opacity-40">{busy ? '가져오는 중…' : `선택한 ${selected.size}개 가져오기`}</button>
    </>}
  </details>;
}

'use client';

import { useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { CONTACT_FIELDS, contactFromRow, importContacts, isContactAccessDenied, mapContactHeaders, parseContactCsv, type ContactField, type ContactMapping, type ImportStatus } from '@/lib/contactImport';

const MAX_FILE = 5 * 1024 * 1024;
const MAX_ROWS = 2000;
const statusLabel: Record<ImportStatus, string> = { created: '추가됨', duplicate: '중복으로 건너뜀', failed: '실패 또는 저장 여부 확인 필요', invalid: '이름 없음' };

export function ContactImport({ accountId, userId, onImported, onDenied }: { accountId: string; userId: string; onImported: () => void; onDenied: () => void }) {
  const [table, setTable] = useState<string[][]>([]);
  const [mapping, setMapping] = useState<ContactMapping>({});
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [statuses, setStatuses] = useState<Record<number, ImportStatus>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const controller = useRef<AbortController | null>(null);
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
      if (file.size > MAX_FILE) throw new Error('파일은 5MB 이하여야 합니다.');
      let rows: string[][];
      if (/\.xlsx$/i.test(file.name)) {
        const [{ readSheet }, { validateContactWorkbook }] = await Promise.all([import('read-excel-file/browser'), import('@/lib/contactExcel')]);
        const bytes = await file.arrayBuffer();
        validateContactWorkbook(new Uint8Array(bytes));
        const sheet = await readSheet(bytes);
        rows = sheet.map((row) => row.map((cell) => cell instanceof Date ? cell.toISOString().slice(0, 10) : cell == null ? '' : String(cell)));
      } else if (/\.csv$/i.test(file.name)) {
        const bytes = await file.arrayBuffer();
        let text: string;
        try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes); }
        catch { text = new TextDecoder('euc-kr').decode(bytes); }
        rows = parseContactCsv(text);
      } else throw new Error('CSV 또는 .xlsx 파일을 선택해 주세요.');
      if (!current()) return;
      if (rows.length < 2) throw new Error('제목 행과 연락처 행이 필요합니다.');
      if (rows.length - 1 > MAX_ROWS || rows.some((row) => row.length > 100)) throw new Error('한 번에 최대 2,000행, 100열까지 가져올 수 있습니다.');
      const headers = rows[0].map((header, i) => header.trim() || `열 ${i + 1}`);
      setTable([headers, ...rows.slice(1)]);
      setMapping(mapContactHeaders(headers));
      setSelected(new Set(rows.slice(1).map((_, i) => i)));
    } catch (err) {
      if (current()) setError(err instanceof Error ? err.message : '파일을 읽지 못했습니다.');
    }
  };

  const submit = async () => {
    const active = controller.current;
    if (!active || active.signal.aborted || busy || mapping.name === undefined) return;
    const indexes = [...selected].filter((i) => statuses[i] !== 'created');
    if (!indexes.length) return;
    setBusy(true); setError('');
    try {
      await importContacts(indexes.map((i) => contactFromRow(table[i + 1], mapping)),
        (cursor) => accountApi.contacts(accountId, userId, cursor, active.signal),
        (contact) => accountApi.createContact(accountId, contact, userId, active.signal),
        (index, status) => { if (!active.signal.aborted) setStatuses((old) => ({ ...old, [indexes[index]]: status })); });
      if (!active.signal.aborted) onImported();
    } catch (err) {
      if (!active.signal.aborted) {
        if (isContactAccessDenied(err)) { onDenied(); return; }
        setError('가져오기가 중단되었습니다. 표시된 결과를 확인하고 다시 시도해 주세요. 저장 여부가 불확실한 항목은 연락처 목록을 확인해 주세요.');
        onImported();
      }
    } finally { if (!active.signal.aborted) setBusy(false); }
  };

  const headers = table[0] || [];
  const unmapped = headers.filter((_, i) => !Object.values(mapping).includes(i));
  return <details className="mt-4 border-t border-slate-200 dark:border-white/10 pt-4">
    <summary className="cursor-pointer text-sm font-semibold">Remember / CSV / Excel 가져오기</summary>
    <p className="mt-2 text-xs text-slate-500">현재 고객에게 추가합니다. CSV 또는 .xlsx 파일을 선택하고 대상 행과 열을 확인하세요. 파일은 5MB, Excel은 압축 해제 후 16MB까지 지원합니다. Excel에서 전화번호는 텍스트 형식이어야 앞자리 0이 보존됩니다.</p>
    <input type="file" accept=".csv,.xlsx" disabled={busy} className="mt-2 text-sm" onChange={(e) => { void chooseFile(e.target.files?.[0]); e.target.value = ''; }} />
    {error && <p role="alert" className="mt-2 text-sm text-red-600">{error}</p>}
    {table.length > 0 && <>
      <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
        {CONTACT_FIELDS.map(([field, label]) => <label key={field} className="text-xs text-slate-600 dark:text-slate-300">{label}{field === 'name' && ' *'}
          <select value={mapping[field] ?? ''} disabled={busy} onChange={(e) => setMapping((old) => {
            const next = { ...old };
            if (e.target.value === '') delete next[field as ContactField]; else next[field as ContactField] = Number(e.target.value);
            return next;
          })} className="mt-1 w-full rounded border border-slate-200 bg-white p-1.5 dark:border-white/10 dark:bg-slate-900">
            <option value="">가져오지 않음</option>{headers.map((header, i) => <option key={i} value={i}>{header}</option>)}
          </select>
        </label>)}
      </div>
      {unmapped.length > 0 && <p className="mt-2 text-xs text-amber-700">연결되지 않은 열: {unmapped.join(', ')}</p>}
      <p className="mt-3 text-xs text-slate-500">이메일 또는 휴대폰·근무처 전화가 같으면 건너뜁니다. 대표번호를 공유하는 사람도 중복으로 표시될 수 있습니다. 동시 가져오기는 중복이 생길 수 있습니다.</p>
      <div className="mt-2 max-h-80 overflow-auto rounded border border-slate-200 dark:border-white/10">
        <table className="w-full text-left text-xs"><thead><tr><th className="p-2"><input aria-label="전체 행 선택" type="checkbox" checked={selected.size === table.length - 1} disabled={busy} onChange={(e) => setSelected(e.target.checked ? new Set(table.slice(1).map((_, i) => i)) : new Set())} /></th><th>이름</th><th>회사</th><th>이메일 / 전화</th><th>결과</th></tr></thead>
          <tbody>{table.slice(1).map((row, i) => { const contact = contactFromRow(row, mapping); return <tr key={i} className="border-t border-slate-100 dark:border-white/5"><td className="p-2"><input aria-label={`${i + 2}행 선택`} type="checkbox" checked={selected.has(i)} disabled={busy || statuses[i] === 'created'} onChange={(e) => setSelected((old) => { const next = new Set(old); if (e.target.checked) next.add(i); else next.delete(i); return next; })} /></td><td className="p-2">{contact.name || '—'}<details><summary className="cursor-pointer text-primary">미리보기</summary><div className="whitespace-pre-wrap">{CONTACT_FIELDS.filter(([field]) => contact[field]).map(([field, label]) => <div key={field}>{label}: {contact[field]}</div>)}</div></details></td><td className="p-2">{contact.company || '—'}</td><td className="p-2">{contact.email || contact.phone || contact.workPhone || '—'}</td><td className="p-2">{statuses[i] ? statusLabel[statuses[i]] : '대기'}</td></tr>; })}</tbody>
        </table>
      </div>
      <button type="button" disabled={busy || mapping.name === undefined || selected.size === 0} onClick={() => void submit()} className="mt-3 rounded bg-primary px-3 py-2 text-sm font-semibold text-white disabled:opacity-40">{busy ? '가져오는 중…' : `선택한 ${selected.size}개 가져오기`}</button>
    </>}
  </details>;
}

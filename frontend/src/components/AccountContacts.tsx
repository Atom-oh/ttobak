'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { CONTACT_FIELDS, isContactAccessDenied } from '@/lib/contactImport';
import type { AccountContact, ContactInput } from '@/types/meeting';
import { ContactImport } from '@/components/ContactImport';

export function AccountContacts({ accountId, userId }: { accountId: string; userId: string }) {
  const [contacts, setContacts] = useState<AccountContact[]>([]);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<string | null>(null);
  const [form, setForm] = useState<(ContactInput & { revision?: string }) | null>(null);
  const [accessDenied, setAccessDenied] = useState(false);
  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);

  const denyAccess = useCallback(() => {
    controller.current?.abort();
    generation.current++;
    setContacts([]); setCursor(''); setForm(null); setEditing(null); setAccessDenied(true);
  }, []);

  const load = useCallback(async (next = '') => {
    const active = controller.current;
    if (!active || active.signal.aborted) return;
    const current = ++generation.current;
    setLoading(true); setError('');
    try {
      const page = await accountApi.contacts(accountId, userId, next, active.signal);
      if (active.signal.aborted || current !== generation.current) return;
      setContacts((old) => next ? [...old, ...page.contacts] : page.contacts);
      setCursor(page.nextCursor || '');
    } catch (err) {
      if (active.signal.aborted || current !== generation.current) return;
      if (isContactAccessDenied(err)) { denyAccess(); return; }
      setContacts([]); setCursor(''); setError('연락처를 불러오지 못했습니다. 권한과 네트워크를 확인해 주세요.');
    } finally { if (!active.signal.aborted && current === generation.current) setLoading(false); }
  }, [accountId, userId, denyAccess]);

  useEffect(() => {
    const active = new AbortController();
    controller.current = active;
    void load();
    return () => { active.abort(); };
  }, [load]);

  const save = async () => {
    const active = controller.current;
    if (!active || !form || !form.name.trim() || saving) return;
    setSaving(true); setError('');
    try {
      if (editing) await accountApi.updateContact(accountId, editing, { ...form, revision: form.revision || '' }, userId, active.signal);
      else await accountApi.createContact(accountId, form, userId, active.signal);
      if (!active.signal.aborted) { setForm(null); setEditing(null); await load(); }
    } catch (err) {
      if (!active.signal.aborted) {
        if (isContactAccessDenied(err)) denyAccess();
        else setError('연락처를 저장하지 못했습니다. 입력과 권한을 확인하고, 다른 팀원이 변경했다면 목록을 새로고침해 주세요.');
      }
    } finally { if (!active.signal.aborted) setSaving(false); }
  };

  const remove = async (contact: AccountContact) => {
    const active = controller.current;
    if (!active || saving || !window.confirm(`${contact.name} 연락처를 삭제할까요?`)) return;
    setSaving(true); setError('');
    try {
      await accountApi.deleteContact(accountId, contact.contactId, contact.revision, userId, active.signal);
      if (!active.signal.aborted) await load();
    } catch (err) { if (!active.signal.aborted) { if (isContactAccessDenied(err)) denyAccess(); else setError('연락처를 삭제하지 못했습니다.'); } }
    finally { if (!active.signal.aborted) setSaving(false); }
  };

  if (accessDenied) return <section className="mb-8"><p role="alert" className="text-sm text-red-600">고객 연락처에 대한 접근 권한이 없습니다.</p></section>;

  return <section className="mb-8">
    <div className="mb-3 flex items-center justify-between gap-2"><h3 className="text-base font-bold text-slate-900 dark:text-text-main">고객 연락처</h3>
      <div className="flex gap-3"><button type="button" onClick={() => void load()} disabled={loading || saving} className="text-sm text-primary disabled:opacity-40">목록 새로고침</button>
      <button type="button" onClick={() => { setEditing(null); setForm({ name: '' }); }} disabled={saving} className="text-sm font-semibold text-primary disabled:opacity-40">연락처 추가</button></div></div>
    <div className="glass-panel rounded-xl p-4">
      <p className="mb-3 text-xs text-slate-500">이 고객 계정의 팀원이 함께 관리합니다.</p>
      {error && <p role="alert" className="mb-3 text-sm text-red-600">{error} 다른 팀원이 수정한 경우 목록을 새로고침한 뒤 다시 선택해 주세요.</p>}
      {form && <div className="mb-4 rounded-lg border border-slate-200 p-3 dark:border-white/10">
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {CONTACT_FIELDS.map(([field, label]) => <label key={field} className="text-xs text-slate-600 dark:text-slate-300">{label}{field === 'name' && ' *'}
            {field === 'notes' ? <textarea value={form[field] || ''} onChange={(e) => setForm({ ...form, [field]: e.target.value })} maxLength={2000} className="mt-1 w-full rounded border border-slate-200 bg-white p-2 text-sm dark:border-white/10 dark:bg-slate-900" /> :
              <input value={form[field] || ''} onChange={(e) => setForm({ ...form, [field]: e.target.value })} maxLength={field === 'name' ? 100 : 500} className="mt-1 w-full rounded border border-slate-200 bg-white p-2 text-sm dark:border-white/10 dark:bg-slate-900" />}
          </label>)}
        </div>
        <div className="mt-3 flex gap-3"><button type="button" disabled={saving || !form.name.trim()} onClick={() => void save()} className="rounded bg-primary px-3 py-2 text-sm font-semibold text-white disabled:opacity-40">저장</button><button type="button" disabled={saving} onClick={() => { setForm(null); setEditing(null); }} className="text-sm">취소</button></div>
      </div>}
      {contacts.length === 0 && !loading && !error && <p className="text-sm text-slate-500">등록된 연락처가 없습니다.</p>}
      <div className="space-y-2">{contacts.map((contact) => <div key={contact.contactId} className="flex flex-wrap items-start justify-between gap-2 border-b border-slate-100 pb-2 text-sm dark:border-white/5">
        <div><div className="font-semibold">{contact.name} {contact.title && <span className="font-normal text-slate-500">{contact.title}</span>}</div><div className="text-xs text-slate-500">{[contact.company, contact.department, contact.email, contact.phone || contact.workPhone].filter(Boolean).join(' · ')}</div>
          {(contact.address || contact.fax || contact.notes || contact.cardRegisteredAt || contact.cardBook || contact.group || (contact.phone && contact.workPhone)) && <details className="mt-1 text-xs text-slate-500"><summary className="cursor-pointer">상세 정보</summary><div className="mt-1 whitespace-pre-wrap">{CONTACT_FIELDS.filter(([field]) => field !== 'name' && contact[field]).map(([field, label]) => <div key={field}>{label}: {contact[field]}</div>)}</div></details>}
        </div>
        <div className="flex gap-3"><button type="button" disabled={saving} onClick={() => { const { contactId, ...values } = contact; setEditing(contactId); setForm(values); }} className="text-xs text-primary">수정</button><button type="button" disabled={saving} onClick={() => void remove(contact)} className="text-xs text-red-600">삭제</button></div>
      </div>)}</div>
      {loading && <p className="mt-2 text-xs text-slate-500">불러오는 중…</p>}
      {cursor && <button type="button" disabled={loading} onClick={() => void load(cursor)} className="mt-3 text-sm text-primary disabled:opacity-40">연락처 더 보기</button>}
      <ContactImport accountId={accountId} userId={userId} onImported={() => void load()} onDenied={denyAccess} />
    </div>
  </section>;
}

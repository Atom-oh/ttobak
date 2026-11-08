'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi } from '@/lib/api';
import { confirmDialog } from '@/lib/dialog';
import { isContactAccessDenied } from '@/lib/contactImport';
import { addAttendee, EMPTY_EVENT_FORM, eventInputFromForm, formatEventTime, toLocalInput, type EventForm } from '@/lib/accountEvents';
import type { AccountContact, AccountEvent } from '@/types/meeting';

type Scope = 'ahead' | 'past';
const inputClass = 'mt-1 w-full rounded border border-slate-200 bg-white p-2 text-sm dark:border-white/10 dark:bg-slate-900';

/** Shared events (schedules) of one account, for its direct members. */
export function AccountEvents({ accountId, userId }: { accountId: string; userId: string }) {
  const [scope, setScope] = useState<Scope>('ahead');
  const [events, setEvents] = useState<AccountEvent[]>([]);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [accessDenied, setAccessDenied] = useState(false);
  const [form, setForm] = useState<EventForm | null>(null);
  const [editing, setEditing] = useState<AccountEvent | null>(null);
  const [contacts, setContacts] = useState<AccountContact[]>([]);
  const [guest, setGuest] = useState({ name: '', email: '' });
  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);
  // The past range's upper bound is fixed per listing: the server binds it into
  // the continuation cursor, so later pages must send the same value.
  const pastTo = useRef(new Date().toISOString());

  const denyAccess = useCallback(() => {
    controller.current?.abort();
    generation.current++;
    setEvents([]); setCursor(''); setForm(null); setEditing(null); setAccessDenied(true);
  }, []);

  const load = useCallback(async (next = '') => {
    const active = controller.current;
    if (!active || active.signal.aborted) return;
    const current = ++generation.current;
    setLoading(true); setError('');
    try {
      if (!next) pastTo.current = new Date().toISOString();
      const options = scope === 'ahead' ? { cursor: next } : { order: 'desc' as const, to: pastTo.current, cursor: next };
      const page = await accountApi.events(accountId, userId, options, active.signal);
      if (active.signal.aborted || current !== generation.current) return;
      setEvents(old => next ? [...old, ...page.events] : page.events);
      setCursor(page.nextCursor || '');
    } catch (err) {
      if (active.signal.aborted || current !== generation.current) return;
      if (isContactAccessDenied(err)) { denyAccess(); return; }
      setEvents([]); setCursor(''); setError('일정을 불러오지 못했습니다. 권한과 네트워크를 확인해 주세요.');
    } finally { if (!active.signal.aborted && current === generation.current) setLoading(false); }
  }, [accountId, userId, scope, denyAccess]);

  useEffect(() => {
    const active = new AbortController();
    controller.current = active;
    void load();
    return () => { active.abort(); };
  }, [load]);

  // Contacts are offered as attendees; failure only hides the picker.
  useEffect(() => {
    const active = new AbortController();
    accountApi.contacts(accountId, userId, '', active.signal)
      .then(page => { if (!active.signal.aborted) setContacts(page.contacts); })
      .catch(() => { if (!active.signal.aborted) setContacts([]); });
    return () => active.abort();
  }, [accountId, userId]);

  const startEdit = (event: AccountEvent | null) => {
    setError(''); setGuest({ name: '', email: '' });
    setEditing(event);
    setForm(event ? {
      title: event.title, start: toLocalInput(event.startAt), end: toLocalInput(event.endAt), allDay: !!event.allDay,
      location: event.location || '', notes: event.notes || '', attendees: event.attendees || [],
    } : { ...EMPTY_EVENT_FORM });
  };

  const save = async () => {
    const active = controller.current;
    if (!active || !form || saving) return;
    const { input, error: invalid } = eventInputFromForm(form, editing?.revision);
    if (!input) { setError(invalid || '입력을 확인해 주세요.'); return; }
    // Untouched times keep their stored instant; a datetime-local round trip
    // can shift an ambiguous (repeated DST) local hour.
    if (editing && form.start === toLocalInput(editing.startAt)) input.startAt = editing.startAt;
    if (editing?.endAt && form.end === toLocalInput(editing.endAt)) input.endAt = editing.endAt;
    setSaving(true); setError('');
    try {
      if (editing) await accountApi.updateEvent(accountId, editing.eventId, { ...input, revision: editing.revision }, userId, active.signal);
      else await accountApi.createEvent(accountId, input, userId, active.signal);
      if (!active.signal.aborted) { setForm(null); setEditing(null); await load(); }
    } catch (err) {
      if (active.signal.aborted) return;
      if (isContactAccessDenied(err)) denyAccess();
      else setError('일정을 저장하지 못했습니다. 다른 팀원이 수정했다면 목록을 새로고침한 뒤 다시 시도해 주세요.');
    } finally { if (!active.signal.aborted) setSaving(false); }
  };

  const remove = async (event: AccountEvent) => {
    const active = controller.current;
    if (!active || active.signal.aborted || saving) return;
    setSaving(true); setError('');
    try {
      if (!await confirmDialog(`'${event.title}' 일정을 삭제할까요?`) || active.signal.aborted) return;
      await accountApi.deleteEvent(accountId, event.eventId, event.revision, userId, active.signal);
      if (!active.signal.aborted) await load();
    } catch (err) {
      if (!active.signal.aborted) { if (isContactAccessDenied(err)) denyAccess(); else setError('일정을 삭제하지 못했습니다.'); }
    } finally { if (!active.signal.aborted) setSaving(false); }
  };

  if (accessDenied) return <section className="mb-8"><p role="alert" className="text-sm text-red-600">이 고객의 일정에 대한 접근 권한이 없습니다.</p></section>;

  return <section className="mb-8" aria-labelledby="account-events-heading">
    <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
      <h3 id="account-events-heading" className="text-base font-bold text-slate-900 dark:text-text-main">고객 일정</h3>
      <div className="flex gap-3">
        <button type="button" onClick={() => void load()} disabled={loading || saving} className="text-sm text-primary disabled:opacity-40">새로고침</button>
        <button type="button" onClick={() => startEdit(null)} disabled={saving} className="text-sm font-semibold text-primary disabled:opacity-40">일정 추가</button>
      </div>
    </div>
    <div className="glass-panel rounded-xl p-4">
      <p className="mb-3 text-xs text-slate-500">이 고객 계정의 팀원이 함께 보는 일정입니다. MCP로도 추가할 수 있습니다.</p>
      <div role="tablist" aria-label="일정 범위" className="mb-3 flex gap-2">
        {(['ahead', 'past'] as const).map(value => <button key={value} type="button" role="tab" aria-selected={scope === value}
          // Switching would abort an in-flight save or delete with the listing,
          // and re-selecting the active range must not blank a loaded list.
          disabled={saving}
          onClick={() => { if (value === scope || saving) return; setScope(value); setEvents([]); setCursor(''); }}
          className={`rounded-lg px-3 py-1.5 text-sm ${scope === value ? 'bg-primary text-white' : 'text-slate-600 dark:text-text-muted'}`}>
          {value === 'ahead' ? '다가오는 일정' : '지난 일정'}</button>)}
      </div>
      {error && <p role="alert" className="mb-3 text-sm text-red-600">{error}</p>}
      {form && <div className="mb-4 rounded-lg border border-slate-200 p-3 dark:border-white/10">
        <div className="grid gap-2 sm:grid-cols-2">
          <label className="text-xs text-slate-600 dark:text-slate-300 sm:col-span-2">제목 *<input value={form.title} maxLength={200} onChange={e => setForm({ ...form, title: e.target.value })} className={inputClass} /></label>
          <label className="text-xs text-slate-600 dark:text-slate-300">시작 *<input type="datetime-local" value={form.start} onChange={e => setForm({ ...form, start: e.target.value })} className={inputClass} /></label>
          <label className="text-xs text-slate-600 dark:text-slate-300">종료<input type="datetime-local" value={form.end} onChange={e => setForm({ ...form, end: e.target.value })} className={inputClass} /></label>
          <label className="flex items-center gap-2 text-xs text-slate-600 dark:text-slate-300"><input type="checkbox" checked={form.allDay} onChange={e => setForm({ ...form, allDay: e.target.checked })} />종일</label>
          <label className="text-xs text-slate-600 dark:text-slate-300 sm:col-span-2">장소 / 화상회의 링크<input value={form.location} maxLength={500} onChange={e => setForm({ ...form, location: e.target.value })} className={inputClass} /></label>
          <div className="text-xs text-slate-600 dark:text-slate-300 sm:col-span-2">참석자
            <div className="mt-1 flex flex-wrap gap-2">{form.attendees.map((a, i) => <span key={`${a.contactId || a.name}-${i}`} className="rounded-full bg-primary/10 px-2 py-1 text-primary">
              {a.name}{a.email ? ` <${a.email}>` : ''}<button type="button" aria-label={`${a.name} 참석자 제거`} className="ml-1" onClick={() => setForm({ ...form, attendees: form.attendees.filter((_, j) => j !== i) })}>×</button></span>)}</div>
            {contacts.length > 0 && <select aria-label="연락처에서 참석자 추가" value="" className={inputClass} onChange={e => {
              const contact = contacts.find(c => c.contactId === e.target.value);
              if (contact) setForm({ ...form, attendees: addAttendee(form.attendees, { contactId: contact.contactId, name: contact.name, ...(contact.email ? { email: contact.email } : {}) }) });
            }}><option value="">연락처에서 추가…</option>{contacts.map(c => <option key={c.contactId} value={c.contactId}>{c.name}{c.company ? ` (${c.company})` : ''}</option>)}</select>}
            <div className="mt-2 flex flex-wrap gap-2">
              <input aria-label="참석자 이름" placeholder="이름" value={guest.name} maxLength={100} onChange={e => setGuest({ ...guest, name: e.target.value })} className={`${inputClass} mt-0 flex-1`} />
              <input aria-label="참석자 이메일" placeholder="이메일 (선택)" value={guest.email} maxLength={200} onChange={e => setGuest({ ...guest, email: e.target.value })} className={`${inputClass} mt-0 flex-1`} />
              <button type="button" disabled={!guest.name.trim()} onClick={() => { setForm({ ...form, attendees: addAttendee(form.attendees, { name: guest.name.trim(), ...(guest.email.trim() ? { email: guest.email.trim() } : {}) }) }); setGuest({ name: '', email: '' }); }} className="text-sm text-primary disabled:opacity-40">추가</button>
            </div>
          </div>
          <label className="text-xs text-slate-600 dark:text-slate-300 sm:col-span-2">메모<textarea value={form.notes} maxLength={4000} onChange={e => setForm({ ...form, notes: e.target.value })} className={inputClass} /></label>
        </div>
        <div className="mt-3 flex gap-3">
          <button type="button" disabled={saving || !form.title.trim() || !form.start} onClick={() => void save()} className="rounded bg-primary px-3 py-2 text-sm font-semibold text-white disabled:opacity-40">저장</button>
          <button type="button" disabled={saving} onClick={() => { setForm(null); setEditing(null); }} className="text-sm">취소</button>
        </div>
      </div>}
      {events.length === 0 && !loading && !error && <p className="text-sm text-slate-500">{scope === 'ahead' ? '예정된 일정이 없습니다.' : '지난 일정이 없습니다.'}</p>}
      <ul className="space-y-2">{events.map(event => <li key={event.eventId} className="flex flex-wrap items-start justify-between gap-2 border-b border-slate-100 pb-2 text-sm dark:border-white/5">
        <div className="min-w-0">
          <div className="font-semibold">{event.title}</div>
          <div className="text-xs text-slate-500">{formatEventTime(event.startAt, event.endAt, event.allDay)}{event.location ? ` · ${event.location}` : ''}</div>
          {event.attendees?.length > 0 && <div className="mt-1 text-xs text-slate-500">참석자: {event.attendees.map(a => a.name).join(', ')}</div>}
          {event.notes && <details className="mt-1 text-xs text-slate-500"><summary className="cursor-pointer">메모</summary><div className="mt-1 whitespace-pre-wrap">{event.notes}</div></details>}
        </div>
        <div className="flex gap-3">
          <button type="button" disabled={saving} onClick={() => startEdit(event)} className="text-xs text-primary">수정</button>
          <button type="button" disabled={saving} onClick={() => void remove(event)} className="text-xs text-red-600">삭제</button>
        </div>
      </li>)}</ul>
      {loading && <p className="mt-2 text-xs text-slate-500">불러오는 중…</p>}
      {cursor && <button type="button" disabled={loading} onClick={() => void load(cursor)} className="mt-3 text-sm text-primary disabled:opacity-40">일정 더 보기</button>}
    </div>
  </section>;
}

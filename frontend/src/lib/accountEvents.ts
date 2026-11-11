import type { AccountEventInput, EventAttendee } from '@/types/meeting';

/** Editable form state; times are `datetime-local` values in the viewer's zone. */
export interface EventForm {
  title: string;
  start: string;
  end: string;
  allDay: boolean;
  location: string;
  notes: string;
  attendees: EventAttendee[];
}

export const EMPTY_EVENT_FORM: EventForm = { title: '', start: '', end: '', allDay: false, location: '', notes: '', attendees: [] };

const pad = (n: number) => String(n).padStart(2, '0');

/** RFC 3339 (any offset) to a `datetime-local` value in the viewer's zone. */
export function toLocalInput(rfc3339: string | undefined): string {
  if (!rfc3339) return '';
  const date = new Date(rfc3339);
  if (Number.isNaN(date.getTime())) return '';
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** A `datetime-local` value (viewer's zone) to RFC 3339 UTC, or null if invalid. */
export function fromLocalInput(value: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date.toISOString().replace(/\.\d{3}Z$/, 'Z');
}

const bytes = (value: string) => new TextEncoder().encode(value).length;

/** Validates like the API (ADR-021 contact encryption limits) and builds the request body. */
export function eventInputFromForm(form: EventForm, revision?: string): { input?: AccountEventInput; error?: string } {
  const title = form.title.trim();
  if (!title) return { error: '제목을 입력해 주세요.' };
  if (bytes(title) > 256) return { error: '제목이 너무 깁니다.' };
  const startAt = fromLocalInput(form.start);
  if (!startAt) return { error: '시작 시각을 입력해 주세요.' };
  let endAt: string | undefined;
  if (form.end) {
    const end = fromLocalInput(form.end);
    if (!end) return { error: '종료 시각을 확인해 주세요.' };
    const span = Date.parse(end) - Date.parse(startAt);
    if (span < 0) return { error: '종료 시각은 시작 시각 이후여야 합니다.' };
    if (span > 31 * 24 * 3600 * 1000) return { error: '일정은 최대 31일까지 지정할 수 있습니다.' };
    endAt = end;
  }
  if (bytes(form.location) > 512) return { error: '장소가 너무 깁니다.' };
  if (bytes(form.notes) > 8192) return { error: '메모가 너무 깁니다.' };
  if (form.attendees.length > 50) return { error: '참석자는 최대 50명입니다.' };
  const attendees = form.attendees.map(a => ({
    ...(a.contactId ? { contactId: a.contactId } : {}), name: a.name.trim(), ...(a.email?.trim() ? { email: a.email.trim() } : {}),
  }));
  if (attendees.some(a => !a.name || bytes(a.name) > 256 || (a.email && (!a.email.includes('@') || bytes(a.email) > 256)))) {
    return { error: '참석자 이름과 이메일을 확인해 주세요.' };
  }
  const input: AccountEventInput = {
    ...(revision ? { revision } : {}), title, startAt, ...(endAt ? { endAt } : {}), allDay: form.allDay,
    location: form.location.trim(), attendees, notes: form.notes,
  };
  if (bytes(JSON.stringify(input)) > 12000) return { error: '일정 내용이 너무 깁니다. 메모나 참석자를 줄여 주세요.' };
  return { input };
}

/** Adds an attendee once; contacts dedupe by contactId, others by name and email. */
export function addAttendee(list: EventAttendee[], attendee: EventAttendee): EventAttendee[] {
  const key = (a: EventAttendee) => a.contactId || `${a.name.trim().toLowerCase()}|${(a.email || '').trim().toLowerCase()}`;
  return list.some(a => key(a) === key(attendee)) ? list : [...list, attendee];
}

/** Display text for an event's time in the viewer's zone. */
export function formatEventTime(startAt: string, endAt?: string, allDay = false): string {
  const start = new Date(startAt);
  const date = start.toLocaleDateString('ko-KR', { year: 'numeric', month: 'short', day: 'numeric', weekday: 'short' });
  if (allDay) return `${date} 종일`;
  const time = (d: Date) => d.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
  if (!endAt) return `${date} ${time(start)}`;
  const end = new Date(endAt);
  const sameDay = end.toDateString() === start.toDateString();
  return sameDay ? `${date} ${time(start)}–${time(end)}` : `${date} ${time(start)} – ${end.toLocaleDateString('ko-KR', { month: 'short', day: 'numeric' })} ${time(end)}`;
}

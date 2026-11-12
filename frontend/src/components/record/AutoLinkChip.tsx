'use client';

import { autoLinkedLabel } from '@/lib/ongoingEvent';

/** Shows an Account linked from the ongoing calendar event, with a change
 * button unless the link can no longer be cleared (`onChange` omitted). The
 * button waits (`pending`) while the link to an existing draft is in flight. */
export function AutoLinkChip({ name, onChange, pending = false }: { name: string; onChange?: () => void; pending?: boolean }) {
  return (
    <div role="status" className="flex max-w-full flex-wrap items-center gap-2 rounded-full bg-primary/10 px-3 py-1 text-xs font-medium text-primary">
      <span className="material-symbols-outlined text-sm" aria-hidden="true">event</span>
      <span className="min-w-0 break-words">{autoLinkedLabel(name)}</span>
      {onChange && <button type="button" onClick={onChange} disabled={pending}
        className="rounded-full px-2 py-0.5 font-semibold underline underline-offset-2 hover:bg-primary/10 focus-visible:outline-2 focus-visible:outline-primary disabled:cursor-wait disabled:opacity-50">
        {pending ? '연결 중…' : '변경'}
      </button>}
    </div>
  );
}

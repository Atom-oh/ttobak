'use client';

import {
  AUTO_LINK_BADGE, TRUNCATED_NOTICE, autoLinkTitle, daysInStageText, flowResultLabel, flowResultTone,
  LINK_KIND_EVENT, flowUnlinkInput, isAutoLink, shortDate, stallLabel,
} from '@/lib/accountOverview';
import { stageLabel, UNSET_STAGE_LABEL } from '@/lib/projectStages';
import { useState } from 'react';
import type { AccountFlow as Flow, FlowItem, FlowRow } from '@/types/meeting';
import { AccountGraph } from './AccountGraph';
import { LinkToOpportunity, UnlinkButton } from './LinkToOpportunity';

const TONE_CLASS = {
  red: 'bg-error/10 text-error',
  green: 'bg-primary/10 text-primary',
  gray: 'bg-surface-container text-text-secondary',
} as const;

type Actions = { accountId: string; userId: string; onChanged: () => void; onAccessLost: () => void };

function Gap() {
  return <p className="text-xs text-text-muted">없음</p>;
}

function Item({ item, projectId, actions, linkable }: { item: FlowItem; projectId?: string; actions: Actions; linkable?: boolean }) {
  const unlink = flowUnlinkInput(item, projectId);
  return <li className="text-sm">
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="material-symbols-outlined text-base text-text-muted" aria-hidden="true">{item.kind === 'meeting' ? 'groups' : 'calendar_today'}</span>
      <span className="min-w-0 font-medium text-text-main">{item.title}</span>
      {isAutoLink(item.origin) && <span title={autoLinkTitle(item.evidence)}
        className="cursor-help rounded-full bg-primary/10 px-1.5 py-0.5 text-[11px] font-semibold text-primary">{AUTO_LINK_BADGE}</span>}
    </div>
    {item.date && <div className="text-xs text-text-secondary">{shortDate(item.date)}</div>}
    {item.need && <p className="mt-0.5 text-xs text-text-secondary">니즈: {item.need}</p>}
    {unlink && <UnlinkButton accountId={actions.accountId} userId={actions.userId} input={unlink} targetLabel={item.title}
      onUnlinked={actions.onChanged} onAccessLost={actions.onAccessLost} />}
    {linkable && item.kind === LINK_KIND_EVENT && <div className="mt-0.5">
      <LinkToOpportunity accountId={actions.accountId} userId={actions.userId} fromKind={LINK_KIND_EVENT} fromId={item.id}
        onLinked={actions.onChanged} onAccessLost={actions.onAccessLost} />
    </div>}
  </li>;
}

function Column({ title, items, projectId, actions, linkable }: { title: string; items: FlowItem[]; projectId?: string; actions: Actions; linkable?: boolean }) {
  return <div className="min-w-0">
    <h5 className="mb-1 text-xs font-semibold text-text-secondary">{title}</h5>
    {items.length === 0 ? <Gap /> : <ul className="space-y-1.5">
      {items.map(item => <Item key={`${item.kind}:${item.id}`} item={item} projectId={projectId} actions={actions} linkable={linkable} />)}
    </ul>}
  </div>;
}

function Row({ row, actions }: { row: FlowRow; actions: Actions }) {
  const tone = flowResultTone(row.result?.state);
  const oppty = row.oppty;
  return <li className={`glass-panel rounded-xl p-4 ${tone === 'red' ? 'border-l-4 border-l-error' : ''}`}>
    <div className="grid gap-4 md:grid-cols-4">
      <Column title="활동" items={row.activity ?? []} projectId={row.projectId} actions={actions} />
      <Column title="대응" items={row.response ?? []} projectId={row.projectId} actions={actions} />
      <div className="min-w-0">
        <h5 className="mb-1 text-xs font-semibold text-text-secondary">기회</h5>
        {oppty ? <>
          <p className="text-sm font-semibold text-text-main">{oppty.name}</p>
          <p className="text-xs text-text-secondary">
            {stageLabel(oppty.stage, oppty.stageMapped) || UNSET_STAGE_LABEL} · {daysInStageText(oppty.daysInStage ?? null)}
          </p>
          {(row.history ?? []).length > 0 && <ul className="mt-1 space-y-0.5">
            {row.history.map(change => <li key={`${change.changedAt}:${change.to}`} className="text-xs text-text-secondary">
              {shortDate(change.changedAt)} {change.from || UNSET_STAGE_LABEL} → {change.to || UNSET_STAGE_LABEL}
            </li>)}
          </ul>}
        </> : <Gap />}
      </div>
      <div className="min-w-0">
        <h5 className="mb-1 text-xs font-semibold text-text-secondary">결과</h5>
        <span className={`inline-block rounded-full px-2 py-0.5 text-xs font-semibold ${TONE_CLASS[tone]}`}>{flowResultLabel(row.result?.state)}</span>
        {(row.result?.stall ?? []).length > 0 && <ul className="mt-1 space-y-0.5">
          {row.result.stall.map(reason => <li key={reason.code} className="text-xs text-error">
            {stallLabel(reason.code)}{reason.detail ? ` — ${reason.detail}` : ''}
          </li>)}
        </ul>}
      </div>
    </div>
  </li>;
}

/** Per-opportunity engagement flow: activity → response → opportunity → result. */
export function AccountFlow({ flow, ...actions }: { flow: Flow } & Actions) {
  const rows = flow.rows ?? [];
  const unassignedEvents = flow.unassigned?.activity ?? [];
  const unassignedMeetings = flow.unassigned?.response ?? [];
  const [showGraph, setShowGraph] = useState(false);
  return <section className="mb-8" aria-labelledby="account-flow-heading">
    <h3 id="account-flow-heading" className="mb-3 text-base font-bold text-text-main">기회별 흐름</h3>
    {flow.truncated && <p role="status" className="mb-2 text-xs text-text-secondary">{TRUNCATED_NOTICE}</p>}
    {rows.length === 0 && <p className="mb-3 text-sm text-text-secondary">연결된 기회가 없습니다.</p>}
    <ul className="space-y-3">
      {rows.map(row => <Row key={row.projectId} row={row} actions={actions} />)}
    </ul>
    {(unassignedEvents.length > 0 || unassignedMeetings.length > 0) && <div className="glass-panel mt-3 rounded-xl p-4">
      <h4 className="mb-2 text-sm font-semibold text-text-main">기회 미지정</h4>
      <div className="grid gap-4 md:grid-cols-2">
        <Column title="활동" items={unassignedEvents} actions={actions} linkable />
        <Column title="대응" items={unassignedMeetings} actions={actions} />
      </div>
    </div>}
    <div className="mt-4">
      <button type="button" aria-expanded={showGraph} onClick={() => setShowGraph(v => !v)}
        className="rounded-lg border border-border-light px-3 py-1.5 text-sm font-medium text-text-main hover:bg-surface-container">
        {showGraph ? '관계 숨기기' : '전체 관계 보기'}
      </button>
      {showGraph && <div className="mt-3">
        <AccountGraph accountId={actions.accountId} userId={actions.userId} refreshKey={flow} />
      </div>}
    </div>
  </section>;
}

'use client';

import {
  CANDIDATE_BADGE, LINK_KIND_NEWS, LINK_KIND_PROJECT, TRUNCATED_NOTICE, daysInStageText, newsStateText, projectName, restrictedNotice,
  insightTypeLabel, safeHttpUrl, shortDate, stallLabel,
} from '@/lib/accountOverview';
import { stageLabel, UNSET_STAGE_LABEL } from '@/lib/projectStages';
import type { AccountSignals as Signals } from '@/types/meeting';
import { AccountNewsSources } from './AccountNewsSources';
import { LinkToOpportunity, UnlinkButton } from './LinkToOpportunity';

/**
 * AWS signals: the account's recent news (left) and opportunity signals
 * (right). All text is rendered as plain React text.
 */
export function AccountSignals({ accountId, userId, accountName, signals, onChanged, onAccessLost }: {
  accountId: string;
  userId: string;
  accountName: string;
  signals: Signals;
  onChanged: () => void;
  onAccessLost: () => void;
}) {
  const news = signals.news ?? [];
  const opportunities = signals.opportunities ?? [];
  const insights = signals.insights ?? [];
  const emptyNews = newsStateText(signals.newsState);
  const restricted = restrictedNotice(signals.restrictedCount);

  return <section className="mb-8" aria-labelledby="account-signals-heading">
    <h3 id="account-signals-heading" className="mb-3 text-base font-bold text-text-main">AWS 시그널</h3>
    {signals.linksTruncated && <p role="status" className="mb-2 text-xs text-text-secondary">{TRUNCATED_NOTICE}</p>}
    <div className="grid gap-4 lg:grid-cols-2">
      <div className="glass-panel rounded-xl p-4">
        <h4 className="mb-2 text-sm font-semibold text-text-main">최근 뉴스</h4>
        {emptyNews && <p className="text-sm text-text-secondary">{emptyNews}</p>}
        <ul className="space-y-3">
          {news.map(item => {
            const href = safeHttpUrl(item.url);
            return <li key={item.id} className="border-b border-border-subtle pb-2 text-sm last:border-0">
              <div className="flex flex-wrap items-start gap-2">
                {href
                  ? <a href={href} target="_blank" rel="noopener noreferrer" className="min-w-0 font-semibold text-text-main hover:text-primary">{item.title}</a>
                  : <span className="min-w-0 font-semibold text-text-main">{item.title}</span>}
                {item.candidate && <span className="shrink-0 rounded-full bg-primary/10 px-2 py-0.5 text-xs font-semibold text-primary">{CANDIDATE_BADGE}</span>}
              </div>
              <div className="mt-1 text-xs text-text-secondary">
                {shortDate(item.pubDate)}{item.awsRelevance ? `${item.pubDate ? ' · ' : ''}${item.awsRelevance}` : ''}
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-2">
                {(item.linkedProjectIds ?? []).map(projectId => {
                  const name = projectName(opportunities, projectId);
                  return <span key={projectId} className="inline-flex items-center gap-1 rounded-full bg-surface-container px-2 py-0.5 text-xs text-text-main">
                    {name}
                    <UnlinkButton accountId={accountId} userId={userId} targetLabel={name}
                      input={{ fromKind: LINK_KIND_NEWS, fromId: item.id, toKind: LINK_KIND_PROJECT, toId: projectId }}
                      onUnlinked={onChanged} onAccessLost={onAccessLost} />
                  </span>;
                })}
                <LinkToOpportunity accountId={accountId} userId={userId} fromKind={LINK_KIND_NEWS} fromId={item.id}
                  excludeProjectIds={item.linkedProjectIds ?? []} onLinked={onChanged} onAccessLost={onAccessLost} />
              </div>
            </li>;
          })}
        </ul>
        <AccountNewsSources accountId={accountId} userId={userId} accountName={accountName}
          onChanged={onChanged} onAccessLost={onAccessLost} />
      </div>

      <div className="glass-panel rounded-xl p-4">
        <h4 className="mb-2 text-sm font-semibold text-text-main">기회 시그널</h4>
        {opportunities.length === 0 && <p className="text-sm text-text-secondary">연결된 기회가 없습니다.</p>}
        <ul className="space-y-2">
          {opportunities.map(o => <li key={o.projectId} className="text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-semibold text-text-main">{o.name}</span>
              <span className="rounded-full bg-surface-container px-2 py-0.5 text-xs text-text-secondary">
                {stageLabel(o.stage, o.stageMapped) || UNSET_STAGE_LABEL}
              </span>
              <span className="text-xs text-text-secondary">{daysInStageText(o.daysInStage ?? null)}</span>
            </div>
            {(o.stall ?? []).length > 0 && <ul className="mt-1 space-y-0.5">
              {o.stall.map(reason => <li key={reason.code} className="text-xs text-error">
                {stallLabel(reason.code)}{reason.detail ? ` — ${reason.detail}` : ''}
              </li>)}
            </ul>}
          </li>)}
        </ul>
        {(insights.length > 0 || restricted) && <h4 className="mb-2 mt-4 text-sm font-semibold text-text-main">최근 인사이트</h4>}
        {restricted && <p className="mb-2 text-xs text-text-secondary">{restricted}</p>}
        <ul className="space-y-1">
          {insights.map((insight, index) => <li key={`${insight.sourceId}:${insight.occurredAt}:${index}`} className="text-sm text-text-main">
            <span className="mr-2 rounded bg-surface-container px-1.5 py-0.5 text-xs text-text-secondary">{insightTypeLabel(insight.type)}</span>
            {insight.text}
          </li>)}
        </ul>
      </div>
    </div>
  </section>;
}

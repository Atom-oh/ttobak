'use client';

import { useTeamSync } from '@/hooks/useTeamSync';
import {
  TEAM_SYNC_BUDGET_NOTE, TEAM_SYNC_CANCEL_LABEL, TEAM_SYNC_CLOSE_LABEL, TEAM_SYNC_CONFLICTS_LABEL,
  TEAM_SYNC_NOT_FOUND_LABEL, TEAM_SYNC_NO_CHANGES_LABEL, TEAM_SYNC_REJECTED_LABEL, TEAM_SYNC_RUNNING_LABEL,
  TEAM_SYNC_UPDATED_LABEL, aliasLookup, canApply, teamSyncApplyLabel, teamSyncMessage,
  teamSyncUnchangedLabel, teamValue, TEAM_SYNC_BUTTON_LABEL,
} from '@/lib/teamSync';

const BUTTON = 'px-3 py-1.5 text-sm font-semibold rounded-lg border border-slate-200 dark:border-white/10 text-slate-700 dark:text-text-secondary hover:bg-slate-100 dark:hover:bg-white/5 transition-colors disabled:opacity-40';
const PRIMARY = 'px-3 py-1.5 text-sm font-semibold rounded-lg bg-primary text-white hover:opacity-90 transition-opacity disabled:opacity-40';

/** Sync control and preview/result view; render only for admins in the Mac app. */
export function TeamSyncPanel({ onApplied }: { onApplied: () => void }) {
  const sync = useTeamSync(onApplied);
  if (!sync.available) return null;
  const busy = sync.phase === 'previewing' || sync.phase === 'applying';
  const alias = aliasLookup(sync.preview);

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <button type="button" onClick={sync.runPreview} disabled={busy} className={PRIMARY}>
          {busy ? TEAM_SYNC_RUNNING_LABEL : TEAM_SYNC_BUTTON_LABEL}
        </button>
        {busy && (
          <button type="button" onClick={sync.cancel} className={BUTTON}>{TEAM_SYNC_CANCEL_LABEL}</button>
        )}
      </div>

      {busy && sync.progress.length > 0 && (
        <p className="text-xs text-slate-500 dark:text-text-muted truncate">{sync.progress[sync.progress.length - 1]}</p>
      )}

      {sync.phase === 'error' && (
        <div role="alert" className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
          <p className="text-red-600 dark:text-red-400 text-sm">{teamSyncMessage(sync.errorCode)}</p>
        </div>
      )}

      {sync.phase === 'preview' && sync.preview && (
        <div className="glass-panel rounded-xl p-4 space-y-3 text-sm">
          {canApply(sync.preview) ? (
            <ul className="space-y-1 text-slate-900 dark:text-text-main">
              {sync.preview.changes.map((c) => (
                <li key={c.userId}>{c.alias}: {teamValue(c.from)} → {teamValue(c.to)}</li>
              ))}
            </ul>
          ) : (
            <p className="text-slate-600 dark:text-text-muted">{TEAM_SYNC_NO_CHANGES_LABEL}</p>
          )}
          {sync.preview.notFound.length > 0 && (
            <p className="text-slate-600 dark:text-text-muted">
              {TEAM_SYNC_NOT_FOUND_LABEL}: {sync.preview.notFound.join(', ')}
            </p>
          )}
          <p className="text-slate-600 dark:text-text-muted">{teamSyncUnchangedLabel(sync.preview.unchanged)}</p>
          <p className="text-xs text-slate-500 dark:text-text-muted">{TEAM_SYNC_BUDGET_NOTE}</p>
          <div className="flex items-center gap-2">
            {canApply(sync.preview) && (
              <button type="button" onClick={sync.runApply} className={PRIMARY}>
                {teamSyncApplyLabel(sync.preview.changes.length)}
              </button>
            )}
            <button type="button" onClick={sync.dismiss} className={BUTTON}>{TEAM_SYNC_CLOSE_LABEL}</button>
          </div>
        </div>
      )}

      {sync.phase === 'done' && sync.result && (
        <div className="glass-panel rounded-xl p-4 space-y-2 text-sm text-slate-900 dark:text-text-main">
          <p>{TEAM_SYNC_UPDATED_LABEL}: {sync.result.updated.length > 0 ? sync.result.updated.map(alias).join(', ') : TEAM_SYNC_NO_CHANGES_LABEL}</p>
          {sync.result.conflicts.length > 0 && (
            <p>{TEAM_SYNC_CONFLICTS_LABEL}: {sync.result.conflicts.map(alias).join(', ')}</p>
          )}
          {sync.result.rejected.length > 0 && (
            <div>
              <p>{TEAM_SYNC_REJECTED_LABEL}</p>
              <ul className="list-disc pl-5 text-slate-600 dark:text-text-muted">
                {sync.result.rejected.map((r, i) => (
                  <li key={`${r.userId}-${i}`}>{r.userId ? `${alias(r.userId)}: ` : ''}{r.code}</li>
                ))}
              </ul>
            </div>
          )}
          <button type="button" onClick={sync.dismiss} className={BUTTON}>{TEAM_SYNC_CLOSE_LABEL}</button>
        </div>
      )}
    </div>
  );
}

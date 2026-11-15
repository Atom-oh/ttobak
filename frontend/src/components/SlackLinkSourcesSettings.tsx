'use client';

import { useEffect, useRef, useState } from 'react';
import { COPY } from '@/lib/customerUpdates';
import {
  SYNC_OPTIONS_COPY, errorCode, intakeCodeLabel, linkChannelList, linkChannelsOf, linkFailure, linkSelectionProblem,
  visibleLinkChannels, type LinkChannel, type LinkChannelChoice,
} from '@/lib/intake';
import { getIntakeState, isCommandNotFound, listLinkChannels, selectLinkChannels, useIsMacApp } from '@/lib/tauri';

const INPUT_CLASS =
  'w-full px-3 py-2 text-sm bg-slate-100 dark:bg-surface-lowest dark:border dark:border-white/10 dark:text-text-main border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-text-muted placeholder:text-slate-400';

/** Korean text for a rejected links command: reuse the intake code copy where it exists. */
function failureText(err: unknown, saving: boolean): string {
  const code = errorCode(err);
  switch (linkFailure(code, isCommandNotFound(err), saving)) {
    case 'updateApp': return COPY['mac.links.updateApp'];
    case 'adapterOutdated': return SYNC_OPTIONS_COPY.adapterTooOld;
    case 'needSetup': return COPY['mac.links.needSetup'];
    case 'capReached': return COPY['mac.links.capReached'];
    case 'busy': return COPY['mac.links.busy'];
    default: {
      const label = intakeCodeLabel(code);
      return label !== code ? label : COPY['settings.sources.actionFailed'];
    }
  }
}

/**
 * Mac app only: choose the Slack channels whose public links the local intake
 * uploads. Channel ids, names and labels stay in this component's state and the
 * Tauri calls; they are never sent to the API, logged or stored in the browser.
 * `onSaved` reloads the source list after a save attempt (sources may have changed).
 */
export function SlackLinkSourcesSettings({ onSaved }: { onSaved?: () => void | Promise<void> }) {
  const isMac = useIsMacApp();
  const [chosen, setChosen] = useState<LinkChannelChoice[]>([]);
  const [channels, setChannels] = useState<LinkChannel[] | null>(null);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The stored selection read on mount must not replace what the user already edited.
  const edited = useRef(false);

  useEffect(() => {
    if (!isMac) return;
    let active = true;
    getIntakeState()
      .then(state => {
        if (active && state && !edited.current) setChosen(linkChannelsOf(state.selection));
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [isMac]);

  if (!isMac) return null;

  const problem = linkSelectionProblem(chosen);
  const problemText = problem === 'tooMany' ? COPY['mac.links.tooMany']
    : problem === 'label' ? COPY['settings.sources.errorLabel']
      : problem === 'channel' ? COPY['settings.sources.actionFailed'] : null;

  const edit = (update: (prev: LinkChannelChoice[]) => LinkChannelChoice[]) => {
    edited.current = true;
    setChosen(update);
  };

  const load = async () => {
    setBusy(true);
    setError(null);
    try {
      setChannels(linkChannelList(await listLinkChannels()));
    } catch (err) {
      setError(failureText(err, false));
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const stored = await selectLinkChannels(chosen.map(({ id, label }) => ({ id, label: label.trim() })));
      setChosen(linkChannelsOf([{ connector: 'links', channels: stored }]));
    } catch (err) {
      setError(failureText(err, true));
      // A failed save may have stored some channels: show what is stored now.
      try {
        const state = await getIntakeState();
        if (state) setChosen(linkChannelsOf(state.selection));
      } catch { /* keep the edited list */ }
    } finally {
      setBusy(false);
      try { await onSaved?.(); } catch { /* the list shows its own error */ }
    }
  };

  const toggle = (channel: LinkChannel, checked: boolean) => {
    edit(prev => checked
      ? prev.some(item => item.id === channel.id) ? prev : [...prev, { id: channel.id, label: channel.name }]
      : prev.filter(item => item.id !== channel.id));
  };

  const setLabel = (id: string, label: string) => {
    edit(prev => prev.map(item => (item.id === id ? { ...item, label } : item)));
  };

  const nameOf = (item: LinkChannelChoice) => channels?.find(channel => channel.id === item.id)?.name ?? item.label;
  const visible = visibleLinkChannels(channels, query);

  return (
    <div className="space-y-4">
      <h4 className="text-sm font-semibold text-slate-900 dark:text-text-main">{COPY['mac.links.setupLabel']}</h4>

      {error && (
        <div role="alert" className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}

      {chosen.length > 0 && (
        <ul className="space-y-2">
          {chosen.map(item => (
            <li key={item.id} className="flex items-center gap-2">
              <input
                type="text"
                aria-label={`${nameOf(item)} ${COPY['settings.sources.labelLabel']}`}
                value={item.label}
                onChange={event => setLabel(item.id, event.target.value)}
                disabled={busy}
                className={INPUT_CLASS}
              />
              <button
                type="button"
                aria-label={`${nameOf(item)} ${COPY['settings.sources.delete']}`}
                onClick={() => edit(prev => prev.filter(other => other.id !== item.id))}
                disabled={busy}
                className="shrink-0 px-3 py-1.5 text-sm font-medium text-red-600 dark:text-red-400 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-lg transition-colors disabled:opacity-40"
              >
                {COPY['settings.sources.delete']}
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          onClick={load}
          disabled={busy}
          className="px-3 py-1.5 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors disabled:opacity-40"
        >
          {COPY['mac.links.load']}
        </button>
        <button
          type="button"
          onClick={save}
          disabled={busy || problem !== null}
          className="px-4 py-2 bg-primary text-white rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors"
        >
          {COPY['mac.links.save']}
        </button>
      </div>

      {problemText && <p role="alert" className="text-xs text-red-600 dark:text-red-400">{problemText}</p>}

      {channels && (
        <div className="space-y-2">
          <input
            type="search"
            aria-label={COPY['mac.links.search']}
            value={query}
            onChange={event => setQuery(event.target.value)}
            className={INPUT_CLASS}
          />
          <ul className="max-h-64 overflow-y-auto space-y-1">
            {visible.rows.map(channel => (
              <li key={channel.id}>
                <label className="flex items-center gap-2 px-2 py-1 text-sm text-slate-700 dark:text-text-secondary rounded-lg hover:bg-slate-50 dark:hover:bg-white/5">
                  <input
                    type="checkbox"
                    checked={chosen.some(item => item.id === channel.id)}
                    onChange={event => toggle(channel, event.target.checked)}
                    disabled={busy}
                  />
                  <span className="break-all">{channel.name}</span>
                </label>
              </li>
            ))}
          </ul>
          {visible.more && <p className="text-xs text-slate-400 dark:text-text-muted">{COPY['mac.links.narrowSearch']}</p>}
        </div>
      )}

      <p className="text-xs text-slate-400 dark:text-text-muted">{COPY['mac.links.note']}</p>
    </div>
  );
}

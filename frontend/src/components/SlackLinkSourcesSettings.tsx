'use client';

import { useEffect, useRef, useState } from 'react';
import { COPY } from '@/lib/customerUpdates';
import {
  SYNC_OPTIONS_COPY, errorCode, intakeCodeLabel, linkChannelList, linkChannelsOf, linkFailure, linkSelectionProblem,
  linkSelectionSettled, nextLinkDraft, visibleLinkChannels, type LinkChannel, type LinkChannelChoice,
} from '@/lib/intake';
import { getIntakeState, isCommandNotFound, listLinkChannels, onIntakeFinished, selectLinkChannels, useIsMacApp } from '@/lib/tauri';

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
 * uploads. This component makes no API call; channel ids and names stay in its
 * state and the Tauri calls and are never logged or stored in the browser. The
 * label saved for a channel (its name by default) becomes the source label the
 * adapter stores in the user's TTOBAK account.
 * `onSaved` reloads the source list after a save attempt (sources may have changed).
 */
export function SlackLinkSourcesSettings({ onSaved }: { onSaved?: () => void | Promise<void> }) {
  const isMac = useIsMacApp();
  const [chosen, setChosen] = useState<LinkChannelChoice[]>([]);
  const [channels, setChannels] = useState<LinkChannel[] | null>(null);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Load and Save wait until the stored selection is read (success or failure) and no
  // links call is still writing it, so a save cannot drop channels that were never shown.
  const [ready, setReady] = useState(false);
  // The draft differs from the stored selection; `removed` holds channels the user took
  // out in this session. Both reset when a save or a failed save shows the stored list.
  const edited = useRef(false);
  const removed = useRef(new Set<string>());
  // Bumped by a save so a read started before it cannot apply an older selection.
  const generation = useRef(0);

  useEffect(() => {
    if (!isMac) return;
    let active = true;
    const read = (initial: boolean) => {
      const started = generation.current;
      getIntakeState()
        .then(state => {
          if (!active || generation.current !== started) return;
          if (state) {
            const stored = linkChannelsOf(state.selection);
            setChosen(prev => nextLinkDraft(prev, edited.current, stored, removed.current));
          }
          setReady(linkSelectionSettled(state));
        })
        .catch(() => {
          if (active && initial) setReady(true);
        });
    };
    read(true);
    // Any finished intake call may have changed the stored selection (a links save
    // still running when the page was opened, a setup run).
    const stop = onIntakeFinished(() => read(false));
    return () => {
      active = false;
      stop();
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

  const showStored = (stored: LinkChannelChoice[]) => {
    edited.current = false;
    removed.current = new Set();
    setChosen(stored);
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
    generation.current += 1;
    setBusy(true);
    setError(null);
    let attempted = false;
    try {
      // A save replaces the whole stored list: re-read it first so channels stored after
      // this draft began are kept unless the user removed them.
      const current = await getIntakeState();
      if (current && !linkSelectionSettled(current)) {
        setError(COPY['mac.links.busy']);
        return;
      }
      const draft = current ? nextLinkDraft(chosen, true, linkChannelsOf(current.selection), removed.current) : chosen;
      setChosen(draft);
      if (linkSelectionProblem(draft) !== null) return;
      attempted = true;
      const stored = await selectLinkChannels(draft.map(({ id, label }) => ({ id, label: label.trim() })));
      generation.current += 1;
      showStored(linkChannelsOf([{ connector: 'links', channels: stored }]));
    } catch (err) {
      setError(failureText(err, attempted));
      if (attempted) {
        // A failed save may have stored some channels: show what is stored now.
        try {
          const state = await getIntakeState();
          generation.current += 1;
          if (state) showStored(linkChannelsOf(state.selection));
        } catch { /* keep the edited list */ }
      }
    } finally {
      setBusy(false);
      if (attempted) {
        try { await onSaved?.(); } catch { /* the list shows its own error */ }
      }
    }
  };

  const toggle = (channel: LinkChannel, checked: boolean) => {
    if (checked) removed.current.delete(channel.id);
    else removed.current.add(channel.id);
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
                onClick={() => {
                  removed.current.add(item.id);
                  edit(prev => prev.filter(other => other.id !== item.id));
                }}
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
          disabled={busy || !ready}
          className="px-3 py-1.5 text-sm font-medium text-primary hover:bg-primary/10 rounded-lg transition-colors disabled:opacity-40"
        >
          {COPY['mac.links.load']}
        </button>
        <button
          type="button"
          onClick={save}
          disabled={busy || !ready || problem !== null}
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
            disabled={busy}
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

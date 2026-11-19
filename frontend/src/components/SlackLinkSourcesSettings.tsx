'use client';

import { useEffect, useRef, useState } from 'react';
import { COPY } from '@/lib/customerUpdates';
import {
  SYNC_OPTIONS_COPY, errorCode, intakeCodeLabel, linkChannelList, linkChannelsOf, linkFailure, linkSelectionProblem,
  linkSelectionSettled, nextLinkDraft, visibleLinkChannels, type LinkChannel, type LinkChannelChoice,
} from '@/lib/intake';
import { Banner } from '@astryxdesign/core/Banner';
import { Button } from '@astryxdesign/core/Button';
import { CheckboxInput } from '@astryxdesign/core/CheckboxInput';
import { Heading } from '@astryxdesign/core/Heading';
import { HStack } from '@astryxdesign/core/HStack';
import { StackItem } from '@astryxdesign/core/Stack';
import { Text } from '@astryxdesign/core/Text';
import { TextInput } from '@astryxdesign/core/TextInput';
import { VStack } from '@astryxdesign/core/VStack';
import { getIntakeState, isCommandNotFound, listLinkChannels, onIntakeFinished, selectLinkChannels, useIsMacApp } from '@/lib/tauri';


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
    <VStack gap={4}>
      <Heading level={4}>{COPY['mac.links.setupLabel']}</Heading>

      {error && <Banner status="error" title={error} role="alert" />}

      {chosen.length > 0 && (
        <VStack gap={2}>
          {chosen.map(item => (
            <HStack key={item.id} gap={2} vAlign="center">
              <StackItem size="fill">
                <TextInput
                  label={`${nameOf(item)} ${COPY['settings.sources.labelLabel']}`}
                  isLabelHidden
                  value={item.label}
                  onChange={value => setLabel(item.id, value)}
                  isDisabled={busy}
                />
              </StackItem>
              <Button
                size="sm"
                variant="destructive"
                label={`${nameOf(item)} ${COPY['settings.sources.delete']}`}
                onClick={() => {
                  removed.current.add(item.id);
                  edit(prev => prev.filter(other => other.id !== item.id));
                }}
                isDisabled={busy}
              >
                {COPY['settings.sources.delete']}
              </Button>
            </HStack>
          ))}
        </VStack>
      )}

      <HStack gap={2} wrap="wrap">
        <Button variant="ghost" label={COPY['mac.links.load']} onClick={load} isDisabled={busy || !ready} />
        <Button
          variant="primary"
          label={COPY['mac.links.save']}
          onClick={save}
          isDisabled={busy || !ready || problem !== null}
        />
      </HStack>

      {problemText && <Banner status="error" title={problemText} role="alert" />}

      {channels && (
        <VStack gap={2}>
          <TextInput
            type="search"
            label={COPY['mac.links.search']}
            isLabelHidden
            placeholder={COPY['mac.links.search']}
            value={query}
            onChange={setQuery}
            isDisabled={busy}
          />
          {/* Native max-height scroll: ScrollableArea takes only a fixed height. */}
          <div className="max-h-64 overflow-y-auto">
            <VStack gap={1}>
              {visible.rows.map(channel => (
                <CheckboxInput
                  key={channel.id}
                  label={channel.name}
                  value={chosen.some(item => item.id === channel.id)}
                  onChange={checked => toggle(channel, checked)}
                  isDisabled={busy}
                />
              ))}
            </VStack>
          </div>
          {visible.more && <Text type="supporting" size="sm">{COPY['mac.links.narrowSearch']}</Text>}
        </VStack>
      )}

      <Text type="supporting" size="sm" as="p">{COPY['mac.links.note']}</Text>
    </VStack>
  );
}

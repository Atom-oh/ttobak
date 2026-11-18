'use client';

import { useCallback, useEffect, useState } from 'react';
import { confirmDialog } from '@/lib/dialog';
import { customerUpdatesApi } from '@/lib/api';
import {
  COPY,
  SOURCE_KIND_LABELS,
  guessSourceKind,
  sourceAddErrorMessage,
  validateSourceForm,
  type UpdateSource,
  type UpdateSourceKind,
} from '@/lib/customerUpdates';
import { SlackLinkSourcesSettings } from './SlackLinkSourcesSettings';
import { AstryxScope } from './astryx/AstryxScope';
import { Badge } from '@astryxdesign/core/Badge';
import { Banner } from '@astryxdesign/core/Banner';
import { Button } from '@astryxdesign/core/Button';
import { Card } from '@astryxdesign/core/Card';
import { Center } from '@astryxdesign/core/Center';
import { EmptyState } from '@astryxdesign/core/EmptyState';
import { Field } from '@astryxdesign/core/Field';
import { FormLayout } from '@astryxdesign/core/FormLayout';
import { HStack } from '@astryxdesign/core/HStack';
import { List, ListItem } from '@astryxdesign/core/List';
import { SegmentedControl, SegmentedControlItem } from '@astryxdesign/core/SegmentedControl';
import { Spinner } from '@astryxdesign/core/Spinner';
import { TextInput } from '@astryxdesign/core/TextInput';
import { VStack } from '@astryxdesign/core/VStack';

type AddableKind = Exclude<UpdateSourceKind, 'slack'>;
const ADDABLE_KINDS: AddableKind[] = ['rss', 'site', 'youtube'];

export function CustomerUpdateSourcesSettings({ userId }: { userId: string }) {
  const [sources, setSources] = useState<UpdateSource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [url, setUrl] = useState('');
  const [label, setLabel] = useState('');
  const [kind, setKind] = useState<AddableKind>('site');
  const [kindTouched, setKindTouched] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await customerUpdatesApi.listSources({ expectedUserId: userId });
      setSources(data.sources || []);
    } catch {
      setError(COPY['settings.sources.loadFailed']);
    } finally {
      setLoading(false);
    }
  }, [userId]);

  useEffect(() => {
    load();
  }, [load]);

  const onUrlChange = (value: string) => {
    setUrl(value);
    if (!kindTouched) setKind(guessSourceKind(value));
  };

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault();
    // Enter in a field submits the form again while the first add is pending.
    if (adding) return;
    const invalid = validateSourceForm(url, label);
    if (invalid) {
      setError(invalid);
      return;
    }
    setAdding(true);
    setError(null);
    try {
      await customerUpdatesApi.addSource({ kind, url: url.trim(), label: label.trim() }, { expectedUserId: userId });
      setUrl('');
      setLabel('');
      setKind('site');
      setKindTouched(false);
      await load();
    } catch (err) {
      setError(sourceAddErrorMessage(err));
    } finally {
      setAdding(false);
    }
  };

  const handleToggle = async (source: UpdateSource) => {
    setBusyId(source.sourceId);
    setError(null);
    try {
      await customerUpdatesApi.patchSource(source.sourceId, {
        status: source.status === 'active' ? 'disabled' : 'active',
      }, { expectedUserId: userId });
      await load();
    } catch {
      setError(COPY['settings.sources.actionFailed']);
    } finally {
      setBusyId(null);
    }
  };

  const handleDelete = async (source: UpdateSource) => {
    if (!(await confirmDialog(COPY['settings.sources.deleteConfirm']))) return;
    setBusyId(source.sourceId);
    setError(null);
    try {
      await customerUpdatesApi.deleteSource(source.sourceId, { expectedUserId: userId });
      await load();
    } catch {
      setError(COPY['settings.sources.actionFailed']);
    } finally {
      setBusyId(null);
    }
  };

  return (
    <VStack gap={6}>
      <AstryxScope>
        {loading ? (
          <Center padding={10}>
            <Spinner size="xl" />
          </Center>
        ) : (
          <VStack gap={6}>
            {error && <Banner status="error" title={error} />}

            {sources.length === 0 ? (
              <EmptyState title={COPY['settings.sources.empty']} isCompact />
            ) : (
              <Card padding={0}>
                <List hasDividers>
                  {sources.map((source) => {
                    const isSlack = source.kind === 'slack';
                    const active = source.status === 'active';
                    const busy = busyId === source.sourceId;
                    return (
                      <ListItem
                        key={source.sourceId}
                        label={source.label}
                        description={isSlack ? COPY['settings.sources.slackHint'] : undefined}
                        startContent={<Badge label={SOURCE_KIND_LABELS[source.kind]} variant="purple" />}
                        endContent={
                          // A Slack source cannot be paused here, but it can be deleted (for example one
                          // left by another Mac); the Mac registers a still-selected channel again on save.
                          <HStack gap={1}>
                            {!active && <Badge label={COPY['settings.sources.statusDisabled']} />}
                            {!isSlack && (
                              <Button
                                size="sm"
                                variant="ghost"
                                label={active ? COPY['settings.sources.pause'] : COPY['settings.sources.resume']}
                                isDisabled={busy}
                                onClick={() => handleToggle(source)}
                              />
                            )}
                            <Button
                              size="sm"
                              variant="destructive"
                              label={COPY['settings.sources.delete']}
                              isDisabled={busy}
                              onClick={() => handleDelete(source)}
                            />
                          </HStack>
                        }
                      />
                    );
                  })}
                </List>
              </Card>
            )}

            <form onSubmit={handleAdd}>
              <FormLayout>
                <TextInput
                  type="url"
                  label={COPY['settings.sources.urlLabel']}
                  value={url}
                  onChange={onUrlChange}
                  placeholder={COPY['settings.sources.urlPlaceholder']}
                />
                <TextInput
                  label={COPY['settings.sources.labelLabel']}
                  value={label}
                  onChange={setLabel}
                  placeholder={COPY['settings.sources.labelPlaceholder']}
                />
                {/* A segmented control, not Selector: Selector's dropdown needs CSS anchor
                    positioning (Safari 26), which older macOS app WebViews lack. */}
                <Field
                  label={COPY['settings.sources.kindLabel']}
                  inputID="cu-source-kind"
                  labelID="cu-source-kind-label"
                  isGroupLabel
                  description={COPY['settings.sources.backfillHelp']}
                >
                  <SegmentedControl
                    label={COPY['settings.sources.kindLabel']}
                    value={kind}
                    onChange={(value) => {
                      setKind(value as AddableKind);
                      setKindTouched(true);
                    }}
                  >
                    {ADDABLE_KINDS.map((k) => (
                      <SegmentedControlItem key={k} value={k} label={SOURCE_KIND_LABELS[k]} />
                    ))}
                  </SegmentedControl>
                </Field>
                <Button
                  type="submit"
                  variant="primary"
                  label={adding ? COPY['settings.sources.adding'] : COPY['settings.sources.add']}
                  isLoading={adding}
                  isDisabled={!url.trim() || !label.trim()}
                />
              </FormLayout>
            </form>
          </VStack>
        )}
      </AstryxScope>

      {/* Mounted after the first list load, as before, so a Slack save cannot race it. */}
      {!loading && <SlackLinkSourcesSettings onSaved={load} />}
    </VStack>
  );
}

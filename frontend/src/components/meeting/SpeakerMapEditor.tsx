'use client';

import { useState, useMemo, useEffect, useRef } from 'react';
import { getSpeakerColor } from '@/lib/speakerColors';
import { accountApi } from '@/lib/api';
import { bindingsToSave, suggestionScope, visibleSuggestions, type SpeakerPick, type SpeakerSuggestions } from '@/lib/contactSuggestions';
import { SPEAKER_BIND_FAILED, SPEAKER_BINDINGS_HEADING, SPEAKER_UNBIND_LABEL, meetingLinkId } from '@/lib/accountPeople';
import type { MeetingPeopleEdges, PeopleLink } from '@/types/meeting';

interface SpeakerMapEditorProps {
  transcription?: { speaker: string }[];
  content?: string;
  speakerMap?: Record<string, string>;
  onSave: (speakerMap: Record<string, string>) => Promise<void>;
  /** Whisper-only (pyannote diarization) — undefined/'whisper' is eligible, other providers are not. */
  sttProvider?: string;
  /** Multi-part audio isn't supported yet (v1 scope). */
  audioPartCount?: number;
  onRediarize?: (speakerCount: number) => Promise<void>;
  accountId?: string;
  userId?: string;
  membershipRevision?: number;
  /** Speaker-to-person binding needs the owner, the meeting and an account publication (ADR-067). */
  meetingOwnerId?: string;
  meetingId?: string;
  sharedToAccount?: boolean;
}

const UNMAPPED_PATTERN = /^(spk_\d+|화자[A-Z])$/;

function speakerSortKey(label: string): number {
  if (label.startsWith('spk_')) return parseInt(label.replace('spk_', ''));
  if (label.startsWith('화자') && label.length === 3) return label.charCodeAt(2) - 'A'.charCodeAt(0) + 1000;
  return 2000;
}

export function SpeakerMapEditor({ transcription, content, speakerMap: existingSpeakerMap, onSave, sttProvider, audioPartCount, onRediarize, accountId, userId, membershipRevision = 0, meetingOwnerId, meetingId, sharedToAccount }: SpeakerMapEditorProps) {
  const speakers = useMemo(() => {
    const labels = new Set<string>();
    transcription?.forEach((seg) => {
      if (seg.speaker) labels.add(seg.speaker);
    });
    if (labels.size === 0) {
      const matches = content?.match(/(?:spk_\d+|화자[A-Z])/g);
      matches?.forEach((m) => labels.add(m));
      Object.values(existingSpeakerMap || {}).forEach((name) => labels.add(name));
    }
    return Array.from(labels).sort((a, b) => speakerSortKey(a) - speakerSortKey(b));
  }, [transcription, content, existingSpeakerMap]);

  const hasUnmapped = speakers.some((s) => UNMAPPED_PATTERN.test(s));

  const initMapping = () => {
    const init: Record<string, string> = {};
    speakers.forEach((s) => { init[s] = existingSpeakerMap?.[s] || ''; });
    return init;
  };
  const [mapping, setMapping] = useState<Record<string, string>>(initMapping);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [isOpen, setIsOpen] = useState(false);
  const [rediarizeCount, setRediarizeCount] = useState(speakers.length + 1);
  const [rediarizing, setRediarizing] = useState(false);
  const [loadedSuggestions, setLoadedSuggestions] = useState<SpeakerSuggestions | null>(null);
  const [moreLoading, setMoreLoading] = useState('');
  const suggestionController = useRef<AbortController | null>(null);
  const scope = suggestionScope(accountId || '', userId || '', membershipRevision);
  const suggestions = visibleSuggestions(loadedSuggestions, scope);
  const [picks, setPicks] = useState<SpeakerPick[]>([]);
  const [bindNotice, setBindNotice] = useState('');
  const [edges, setEdges] = useState<{ scope: string; value: MeetingPeopleEdges } | null>(null);
  const [edgesRevision, setEdgesRevision] = useState(0);
  const [unbinding, setUnbinding] = useState('');
  const linkId = meetingOwnerId && meetingId ? meetingLinkId(meetingOwnerId, meetingId) : '';
  const bindingActive = !!(accountId && userId && sharedToAccount && linkId);
  const edgesScope = JSON.stringify([scope, linkId]);
  const speakerEdges = bindingActive && edges?.scope === edgesScope
    ? edges.value.edges.filter((edge) => edge.role === 'speaker' && edges.value.labels[`${edge.toKind}:${edge.toId}`]) : [];

  useEffect(() => {
    if (!isOpen || !bindingActive || !accountId || !userId) return;
    const active = new AbortController();
    accountApi.people.edges(accountId, linkId, { signal: active.signal, expectedUserId: userId })
      .then((value) => { if (!active.signal.aborted) setEdges({ scope: edgesScope, value }); })
      // Existing bindings are optional context; free-text names keep working.
      .catch(() => { if (!active.signal.aborted) setEdges(null); });
    return () => active.abort();
  }, [isOpen, bindingActive, accountId, userId, linkId, edgesScope, edgesRevision]);

  const unbind = async (edge: PeopleLink) => {
    if (!accountId || !userId || unbinding) return;
    const key = `${edge.toKind}:${edge.toId}:${edge.sourceRef || ''}`;
    setUnbinding(key);
    setBindNotice('');
    try {
      await accountApi.people.unbind(accountId, { meetingId: edge.meetingId, toKind: edge.toKind, toId: edge.toId, role: edge.role }, { expectedUserId: userId });
    } catch {
      setBindNotice(SPEAKER_BIND_FAILED);
    } finally {
      setUnbinding('');
      setEdgesRevision((value) => value + 1);
    }
  };

  useEffect(() => {
    if (!isOpen || !accountId || !userId) return;
    const active = new AbortController();
    suggestionController.current = active;
    Promise.all([accountApi.speakerTeam(accountId, userId, '', active.signal), accountApi.contacts(accountId, userId, '', active.signal)])
      .then(([team, contacts]) => {
        if (!active.signal.aborted) setLoadedSuggestions({ scope, members: team.members, contacts: contacts.contacts, nextCursor: contacts.nextCursor || '', teamCursor: team.nextCursor || '' });
      })
      .catch(() => {
        if (!active.signal.aborted) setLoadedSuggestions({ scope, members: [], contacts: [], nextCursor: '', teamCursor: '' });
      });
    return () => {
      active.abort();
      setLoadedSuggestions((old) => old?.scope === scope ? null : old);
      setMoreLoading((old) => old.endsWith(scope) ? '' : old);
    };
  }, [isOpen, accountId, userId, scope]);

  const loadMoreSuggestions = async () => {
    const active = suggestionController.current;
    if (!active || active.signal.aborted || !accountId || !userId || !suggestions.nextCursor || moreLoading) return;
    setMoreLoading(`contacts:${scope}`);
    try {
      const page = await accountApi.contacts(accountId, userId, suggestions.nextCursor, active.signal);
      if (!active.signal.aborted) setLoadedSuggestions((old) => old?.scope === scope ? { ...old, contacts: [...old.contacts, ...page.contacts], nextCursor: page.nextCursor || '' } : old);
    } catch {
      if (!active.signal.aborted) setLoadedSuggestions({ scope, members: [], contacts: [], nextCursor: '', teamCursor: '' });
    } finally { if (!active.signal.aborted) setMoreLoading(''); }
  };

  const loadMoreTeam = async () => {
    const active = suggestionController.current;
    if (!active || active.signal.aborted || !accountId || !userId || !suggestions.teamCursor || moreLoading) return;
    setMoreLoading(`team:${scope}`);
    try {
      const page = await accountApi.speakerTeam(accountId, userId, suggestions.teamCursor, active.signal);
      if (!active.signal.aborted) setLoadedSuggestions((old) => old?.scope === scope ? { ...old, members: [...old.members, ...page.members], teamCursor: page.nextCursor || '' } : old);
    } catch {
      if (!active.signal.aborted) setLoadedSuggestions({ scope, members: [], contacts: [], nextCursor: '', teamCursor: '' });
    } finally { if (!active.signal.aborted) setMoreLoading(''); }
  };

  // Re-sync after a rediarize: the server clears speakerMap and the
  // transcript is regenerated with a fresh (possibly different-length)
  // spk_N set, but this component doesn't unmount across that refetch --
  // without this, stale local `mapping` from the OLD diarization would get
  // written back on the next save, undoing the server's clear.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { setMapping(initMapping()); }, [existingSpeakerMap, speakers.join(',')]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { setRediarizeCount(speakers.length + 1); }, [speakers.join(',')]);

  if (speakers.length === 0) return null;

  const canRediarize = !!onRediarize && (sttProvider === undefined || sttProvider === 'whisper') && (audioPartCount === undefined || audioPartCount <= 1);

  const handleRediarize = async () => {
    if (!onRediarize) return;
    // Re-clamp here too: onBlur may not have fired if the button is reached
    // by click/tab straight from the input without leaving focus first.
    const count = Math.max(2, Math.min(20, rediarizeCount || 2));
    setRediarizing(true);
    try {
      await onRediarize(count);
    } finally {
      setRediarizing(false);
    }
  };

  const hasAnyName = Object.values(mapping).some((v) => v.trim());

  const handleSave = async () => {
    const filtered: Record<string, string> = {};
    for (const [label, name] of Object.entries(mapping)) {
      if (name.trim()) filtered[label] = name.trim();
    }
    if (Object.keys(filtered).length === 0) return;
    setSaving(true);
    setSaveError(null);
    setBindNotice('');
    try {
      await onSave(filtered);
      // Names are saved; binding is best-effort on top of the free text.
      const bindings = bindingActive && accountId && userId ? bindingsToSave(filtered, picks) : [];
      if (bindings.length > 0) {
        const results = await Promise.allSettled(bindings.map((pick) => accountApi.people.bind(accountId!, {
          meetingId: linkId, toKind: pick.kind, toId: pick.id, role: 'speaker', sourceRef: pick.label,
        }, { expectedUserId: userId })));
        if (results.some((result) => result.status === 'rejected')) setBindNotice(SPEAKER_BIND_FAILED);
        setEdgesRevision((value) => value + 1);
      }
      setPicks([]);
      setIsOpen(false);
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : '화자 이름을 저장하지 못했습니다. 다시 시도해 주세요.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className={`rounded-xl p-4 mb-6 ${hasUnmapped
      ? 'bg-amber-50 dark:bg-[#1a1520] border border-amber-200 dark:border-amber-900/30'
      : 'bg-slate-50 dark:bg-white/5 border border-slate-200 dark:border-white/10'
    }`}>
      {saveError && <p role="alert" className="mb-3 text-sm text-red-600 dark:text-red-400">{saveError}</p>}
      {bindNotice && <p role="status" className="mb-3 text-sm text-text-secondary">{bindNotice}</p>}
      <button
        onClick={() => { if (isOpen) setLoadedSuggestions(null); setIsOpen(!isOpen); }}
        className="flex items-center gap-2 w-full text-left"
      >
        <span className={`material-symbols-outlined ${hasUnmapped ? 'text-amber-600 dark:text-amber-400' : 'text-green-600 dark:text-green-400'}`}>
          {hasUnmapped ? 'person_edit' : 'group'}
        </span>
        <span className={`font-semibold text-sm ${hasUnmapped ? 'text-amber-800 dark:text-amber-300' : 'text-slate-700 dark:text-slate-300'}`}>
          {hasUnmapped ? '화자 이름 설정' : '참석자'}
        </span>
        <span className="text-xs text-slate-500 dark:text-slate-400 ml-1 truncate">
          {speakers.length}명 · {speakers.map((s) => existingSpeakerMap?.[s] || s).join(', ')}
        </span>
        <span className={`material-symbols-outlined text-sm ml-auto flex-shrink-0 ${hasUnmapped ? 'text-amber-500' : 'text-slate-400'}`}>
          {isOpen ? 'expand_less' : 'expand_more'}
        </span>
      </button>

      {isOpen && (
        <div className="mt-4 space-y-3">
          <p className="text-xs text-slate-500 dark:text-slate-400">
            공유받은 사람도 화자 이름을 변경할 수 있습니다. 읽기 권한에서는 화자 표시와 담당자를 수정하며, 본문에 이미 쓰인 이름과 열람이 제한된 요약은 바꾸지 않습니다.
          </p>
          {speakers.map((label, i) => (
            <div key={label} className="flex items-center gap-3">
              <div
                className="w-8 h-8 rounded-full flex items-center justify-center text-white text-xs font-bold shrink-0"
                style={{ backgroundColor: getSpeakerColor(i).dot }}
              >
                {i + 1}
              </div>
              <span className="text-sm font-mono text-slate-500 dark:text-slate-400 w-20 shrink-0 truncate">{label}</span>
              <span className="text-slate-400">→</span>
              <input
                type="text"
                maxLength={100}
                value={mapping[label] || ''}
                onChange={(e) => setMapping({ ...mapping, [label]: e.target.value })}
                placeholder={existingSpeakerMap?.[label] || '새 이름 입력...'}
                className="min-w-0 flex-1 text-sm px-3 py-1.5 border border-slate-200 dark:border-white/10 rounded-lg bg-white dark:bg-white/5 text-slate-900 dark:text-white placeholder:text-slate-400 focus:ring-2 focus:ring-primary/20 outline-none"
              />
              {(suggestions.members.length > 0 || suggestions.contacts.length > 0) && <select aria-label={`${label} 추천 이름`} value="" onChange={(e) => {
                const [kind, index] = e.target.value.split(':');
                const candidate = kind === 'team' ? suggestions.members[Number(index)] : suggestions.contacts[Number(index)];
                if (!candidate?.name) return;
                setMapping((old) => ({ ...old, [label]: candidate.name }));
                const pick: SpeakerPick | null = kind === 'team' && 'userId' in candidate
                  ? { label, kind: 'member', id: candidate.userId, name: candidate.name.trim() }
                  : kind === 'contact' && 'contactId' in candidate
                    ? { label, kind: 'contact', id: candidate.contactId, name: candidate.name.trim() } : null;
                if (pick) setPicks((old) => [...old, pick]);
              }} className="max-w-40 rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-xs dark:border-white/10 dark:bg-slate-900">
                <option value="">추천 선택</option>
                {suggestions.members.length > 0 && <optgroup label="Account 팀">{suggestions.members.map((member, index) => <option key={member.userId} value={`team:${index}`} disabled={Array.from(member.name).length > 100 || /\p{Cc}/u.test(member.name)}>{member.name} · {member.role} · {member.email}</option>)}</optgroup>}
                {suggestions.contacts.length > 0 && <optgroup label="고객 연락처">{suggestions.contacts.map((contact, index) => <option key={contact.contactId} value={`contact:${index}`} disabled={Array.from(contact.name).length > 100 || /\p{Cc}/u.test(contact.name)}>{[contact.name, contact.company, contact.title].filter(Boolean).join(' · ')}</option>)}</optgroup>}
              </select>}
            </div>
          ))}
          {speakerEdges.length > 0 && <div className="rounded-lg border border-border-subtle p-3">
            <p className="mb-2 text-xs font-semibold text-text-secondary">{SPEAKER_BINDINGS_HEADING}</p>
            <ul className="space-y-1">
              {speakerEdges.map((edge) => {
                const key = `${edge.toKind}:${edge.toId}:${edge.sourceRef || ''}`;
                return <li key={key} className="flex flex-wrap items-center gap-2 text-xs">
                  <span className="font-mono text-text-secondary">{edge.sourceRef || ''}</span>
                  <span className="text-text-secondary">→</span>
                  <span className="min-w-0 break-all text-text-main">{edges!.value.labels[`${edge.toKind}:${edge.toId}`]}</span>
                  <button type="button" disabled={!!unbinding} onClick={() => void unbind(edge)}
                    className="ml-auto text-primary disabled:opacity-40">{SPEAKER_UNBIND_LABEL}</button>
                </li>;
              })}
            </ul>
          </div>}
          {suggestions.teamCursor && <button type="button" disabled={!!moreLoading} onClick={() => void loadMoreTeam()} className="mr-3 text-xs text-primary disabled:opacity-40">계정 팀 더 보기</button>}
          {suggestions.nextCursor && <button type="button" disabled={!!moreLoading} onClick={() => void loadMoreSuggestions()} className="text-xs text-primary disabled:opacity-40">고객 연락처 더 보기</button>}
          <button
            onClick={handleSave}
            disabled={saving || !hasAnyName}
            className="mt-2 px-4 py-2 bg-primary text-white rounded-lg text-sm font-semibold disabled:opacity-40 hover:opacity-90 transition-opacity flex items-center gap-2"
          >
            {saving ? (
              <>
                <span className="animate-spin rounded-full h-4 w-4 border-2 border-white/30 border-t-white" />
                적용 중...
              </>
            ) : (
              <>
                <span className="material-symbols-outlined text-base">find_replace</span>
                일괄 변경
              </>
            )}
          </button>

          {canRediarize && (
            <div className="pt-3 mt-3 border-t border-slate-200 dark:border-white/10">
              <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">
                화자가 더 있는데 {speakers.length}명만 감지됐나요? 화자 수를 지정해서 다시 분석할 수 있습니다.
                다시 분석하면 트랜스크립트와 요약이 재생성되고 위 이름 매핑은 초기화됩니다.
              </p>
              <div className="flex items-center gap-3">
                <input
                  type="number"
                  min={2}
                  max={20}
                  value={rediarizeCount}
                  // Clamp on blur, not on every keystroke -- clamping while typing
                  // "15" would force it to 2 the instant "1" alone is entered.
                  onChange={(e) => setRediarizeCount(parseInt(e.target.value, 10) || 0)}
                  onBlur={() => setRediarizeCount((v) => Math.max(2, Math.min(20, v || 2)))}
                  className="w-20 text-sm px-3 py-1.5 border border-slate-200 dark:border-white/10 rounded-lg bg-white dark:bg-white/5 text-slate-900 dark:text-white outline-none"
                />
                <span className="text-sm text-slate-500 dark:text-slate-400">명으로 재분석</span>
                <button
                  onClick={handleRediarize}
                  disabled={rediarizing}
                  className="ml-auto px-4 py-2 border border-primary text-primary rounded-lg text-sm font-semibold disabled:opacity-40 hover:bg-primary/5 transition-colors flex items-center gap-2"
                >
                  {rediarizing ? (
                    <>
                      <span className="animate-spin rounded-full h-4 w-4 border-2 border-primary/30 border-t-primary" />
                      재분석 시작 중...
                    </>
                  ) : (
                    <>
                      <span className="material-symbols-outlined text-base">refresh</span>
                      화자 재분석
                    </>
                  )}
                </button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

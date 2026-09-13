import { qaSources } from './qaSources';
import type { ActionItem, QASourceDetail } from '@/types/meeting';

export const MAX_MEETING_NOTES = 32000;
export interface MeetingReference {
  id: string;
  kind: 'insight' | 'research' | 'document' | 'meeting' | 'knowledge' | 'news';
  title: string;
  href?: string;
  excerpt?: string;
  caveats?: string[];
}
export interface QuestionDraft { id: number; text: string }
export interface QAReferenceEvidence { sources?: string[]; sourceDetails?: QASourceDetail[] }

export function codePointLength(text: string): number {
  return Array.from(text).length;
}

function inline(text: string): string {
  return text.replace(/[\r\n]+/g, ' ').replace(/[\\`*_[\]<>]/g, '\\$&');
}

/** Keep durable app/HTTP links, never storage keys or executable URL schemes. */
export function safeReferenceHref(value?: string): string | undefined {
  if (!value || value.includes('\\')) return;
  try {
    const url = new URL(value, 'https://ttobak.invalid');
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) return;
    if (value.startsWith('/')) {
      if (value.startsWith('//') || url.origin !== 'https://ttobak.invalid' ||
          !/^\/(?:meeting|accounts|projects|docs|insights|kb)(?:\/|$)/.test(url.pathname)) return;
      return `${url.pathname}${url.search}${url.hash}`;
    }
    // Relative prose and internal source schemes must not become fabricated URLs.
    if (!/^https?:\/\//i.test(value)) return;
    return url.href;
  } catch {
    return;
  }
}

function sourceLink(title: string, value?: string): string {
  const label = inline(title);
  const href = safeReferenceHref(value);
  return href ? `[${label}](<${href.replace(/[<>]/g, (char) => encodeURIComponent(char))}>)` : label;
}

export function referenceMarkdown(reference: MeetingReference): string {
  const label = {
    insight: '고객 인사이트', research: '리서치', document: '문서',
    meeting: '이전 미팅', knowledge: '지식 자료', news: '수집된 동향',
  }[reference.kind];
  const excerpt = Array.from(reference.excerpt || '').slice(0, 1200).join('');
  const shortened = codePointLength(reference.excerpt || '') > 1200;
  const quote = excerpt ? `\n${excerpt.split('\n').map((line) => `> ${line}`).join('\n')}` : '';
  const caveats = [...(reference.caveats || []), ...(shortened ? ['일부 발췌'] : [])];
  return `### 참고 · ${label}\n출처: ${sourceLink(reference.title, reference.href)}${quote}\n` +
    `${caveats.length ? `범위: ${caveats.map(inline).join(' · ')}\n` : ''}` +
    '참고 자료이며 이번 미팅에서 확인된 발언·합의와 구분합니다.';
}

export function qaNoteMarkdown(question: string, answer: string, evidence?: QAReferenceEvidence): string {
  const sources = qaSources(evidence?.sources, evidence?.sourceDetails);
  const citations = sources.map((source) =>
    `- ${sourceLink(source.label, source.href)}${source.caveats.length ? ` (${source.caveats.map(inline).join(', ')})` : ''}`);
  return `### 검토용 Q&A\n**질문:** ${inline(question)}\n\n${answer.trim()}\n\n` +
    `**출처**\n${citations.length ? citations.join('\n') : '- 제공된 출처 없음 · 별도 확인 필요'}\n\n` +
    'AI 답변을 참고로 추가했습니다. 담당자·기한·고객 합의는 원문과 대조합니다.';
}

/** Throw before changing a draft rather than silently truncating its content. */
export function appendMeetingNotes(current: string, addition: string): string {
  const next = `${current.trimEnd()}${current.trim() ? '\n\n' : ''}${addition.trim()}\n`;
  if (codePointLength(next) > MAX_MEETING_NOTES) {
    throw new Error('미팅 메모는 32,000자까지 저장할 수 있습니다. 내용을 줄인 뒤 다시 추가해 주세요.');
  }
  return next;
}

export const SA_PREPARATION_TEMPLATE = `## 미팅 목표
- 이번 미팅에서 확인할 고객 목표:
- 성공 기준:

## 사전 확인
- 현재 환경·워크로드:
- 제약(보안·비용·일정):
- 확인할 가설·질문:

## 논의와 후속 작업
- 확인된 사실과 결정:
- 추가 검증 사항:
- 담당자·기한:`;

export function preparationNotes(context: string): string {
  if (!context.trim()) return '';
  const result = `## 사전 준비 · 참고 맥락\n이번 미팅의 발언·합의가 아닌 준비 메모입니다.\n\n${context.trim()}\n`;
  if (codePointLength(result) > MAX_MEETING_NOTES) throw new Error('사전 준비 내용이 32,000자를 초과했습니다.');
  return result;
}

export function followUpMarkdown(input: {
  meetingId: string; title: string; notes?: string; summary?: string; actions?: ActionItem[];
}): string {
  const actions = (input.actions || []).map((action) =>
    `- [${action.completed ? 'x' : ' '}] ${inline(action.text)} — 담당: ${inline(action.assignee || '미정')} / 기한: ${inline(action.dueDate || '미정')}`);
  return `# ${inline(input.title)} · SA 후속 정리\n\n` +
    `출처 미팅: ${sourceLink(input.title, `/meeting/${encodeURIComponent(input.meetingId)}`)}\n\n` +
    `## 저장된 미팅 요약\n${input.summary?.trim() || '저장된 요약 없음'}\n\n` +
    `## 준비·참고 메모\n${input.notes?.trim() || '저장된 메모 없음'}\n\n` +
    `## 저장된 액션\n${actions.length ? actions.join('\n') : '저장된 액션 없음 · 추출 성공이나 할 일 없음으로 단정하지 않습니다.'}\n\n` +
    '## SA 확인 후 작성\n- 고객 확인이 필요한 사항:\n- 기술 검증·PoC 계획:\n- 다음 미팅 목표:\n\n' +
    '생성 시점의 저장본입니다. 고객 공유 전 최신 원문·권한·담당자·기한을 확인하세요.\n';
}

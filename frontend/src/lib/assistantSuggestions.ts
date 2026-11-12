import type { Meeting } from '@/types/meeting';

export interface AssistantCard {
  label: string;
  icon: string;
  question: string;
  detail: string;
}

type MeetingMetadata = Pick<Meeting, 'title'> & Partial<Pick<Meeting, 'date' | 'createdAt' | 'tags'>>;

function label(value: string | undefined): string {
  return (value || '').replace(/\s+/g, ' ').trim().slice(0, 80);
}

export function assistantCards(meetings: MeetingMetadata[]): AssistantCard[] {
  const recent = meetings.slice(0, 30).sort((a, b) =>
    (Date.parse(b.date || b.createdAt || '') || 0) - (Date.parse(a.date || a.createdAt || '') || 0));
  const title = label(recent[0]?.title);
  const topics = new Map<string, number>();
  for (const meeting of recent) {
    for (const tag of new Set((meeting.tags || []).slice(0, 20).map(label).filter(Boolean))) {
      topics.set(tag, (topics.get(tag) || 0) + 1);
    }
  }
  const topic = [...topics].sort((a, b) => b[1] - a[1])[0]?.[0];
  return [
    {
      label: title ? '최근 미팅 이어보기' : '이번 주 돌아보기', icon: 'summarize',
      question: title ? `「${title}」 미팅의 핵심 내용과 결정 사항을 정리해줘` : '이번 주 내 미팅의 핵심 내용과 결정 사항을 정리해줘',
      detail: title || '최근 미팅에서 중요한 내용을 확인하세요',
    },
    {
      label: topic ? '자주 다룬 주제' : '주요 논의 살펴보기', icon: 'forum',
      question: topic ? `「${topic}」 관련 최근 미팅의 결론과 남은 쟁점을 비교해줘` : '최근 미팅에서 반복해서 논의한 주제와 남은 쟁점을 찾아줘',
      detail: topic || '여러 미팅의 흐름을 함께 살펴보세요',
    },
    {
      label: '후속 작업 확인', icon: 'task_alt',
      question: title ? `「${title}」 미팅의 미완료 액션아이템과 후속 작업을 정리해줘` : '내 미팅의 미완료 액션아이템과 후속 작업을 정리해줘',
      detail: title || '아직 마무리하지 않은 일을 확인하세요',
    },
    {
      label: '다음 미팅 준비', icon: 'calendar_today',
      question: title ? `「${title}」 미팅에 이어 논의할 질문과 확인할 사항을 정리해줘` : '최근 미팅을 바탕으로 다음 미팅에서 확인할 질문을 추천해줘',
      detail: '이전 논의를 다음 대화로 연결하세요',
    },
  ];
}

export function meetingQuestions(meetingTitle?: string): string[] {
  const title = label(meetingTitle);
  return title ? [
    `「${title}」에서 지금까지 나온 핵심 쟁점은?`,
    `「${title}」에서 합의한 내용과 아직 확인할 사항은?`,
    `「${title}」의 다음 논의에 필요한 질문을 추천해줘`,
  ] : [
    '지금 논의 중인 쟁점과 선택지를 정리해줘',
    '지금까지 합의한 내용과 아직 확인할 사항은?',
    '현재 대화에 이어 확인하면 좋을 질문은?',
  ];
}

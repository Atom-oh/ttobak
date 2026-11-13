// Wire types for GET/POST /api/accounts/{accountId}/customer-brief.
// Optional fields are omitted (never null); arrays may arrive as null.
// All text fields are plain text and must render as text nodes only.

export type CustomerBriefRunStatus = 'none' | 'queued' | 'running' | 'succeeded' | 'failed';

export type CustomerBriefSourceKind = 'meeting' | 'insight' | 'news' | 'event';

export interface CustomerBriefSource {
  kind: CustomerBriefSourceKind | string;
  id: string;
  label: string;
}

export interface CustomerBriefStatement {
  text: string;
  sources: CustomerBriefSource[] | null;
}

export interface CustomerBriefContent {
  nextMeeting: { title: string; startsAt: string; source: CustomerBriefSource } | null;
  situation: CustomerBriefStatement | null;
  sinceLast: CustomerBriefStatement[] | null;
  news: (CustomerBriefStatement & { why: string })[] | null;
  questions: CustomerBriefStatement[] | null;
  terms: { term: string; gloss: string; inDictionary: boolean; sources: CustomerBriefSource[] | null }[] | null;
}

export interface CustomerBriefRun {
  status: CustomerBriefRunStatus | string;
  runId?: string;
  requestedAt?: string;
  errorCode?: string;
}

export interface CustomerBriefResponse {
  run: CustomerBriefRun;
  brief: { generatedAt: string; content: CustomerBriefContent } | null;
}

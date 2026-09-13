'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { accountApi, docApi, insightsApi, kbApi, meetingsApi } from '@/lib/api';
import type { MeetingReference } from '@/lib/meetingReferences';

export const referenceCatalogs = [
  { id: 'knowledge', label: '내 KB', account: false },
  { id: 'documents', label: '내·공유 문서', account: false },
  { id: 'meetings', label: '이전 회의', account: false },
  { id: 'insight', label: '고객 인사이트', account: true },
  { id: 'research', label: '고객 리서치', account: true },
  { id: 'accountDocuments', label: '고객 문서', account: true },
  { id: 'news', label: '수집된 Insights', account: false },
] as const;
export type ReferenceCatalog = typeof referenceCatalogs[number]['id'];
export const MAX_REFERENCE_ITEMS = 100;
const PAGE_SIZE = 10;
const MAX_EXCERPT = 600;
const MAX_TITLE = 180;

function bounded(value: string | undefined, limit: number) {
  const text = (value || '').trim();
  if (text.length <= limit) return text;
  const end = /[\uD800-\uDBFF]/.test(text[limit - 1]) ? limit - 1 : limit;
  return `${text.slice(0, end)}…`;
}

function reference(value: MeetingReference): MeetingReference {
  return {
    ...value,
    title: bounded(value.title, MAX_TITLE) || '제목 없는 자료',
    excerpt: bounded(value.excerpt, MAX_EXCERPT),
    caveats: [
      ...(value.caveats || []),
      ...((value.excerpt?.trim().length || 0) > MAX_EXCERPT ? ['일부 발췌'] : []),
    ],
  };
}

interface CatalogPage {
  items: MeetingReference[];
  next?: string;
}

// Only authenticated list APIs: no document-body, model, or web-search reads.
async function readPage(catalog: ReferenceCatalog, accountId: string, userId?: string, cursor?: string): Promise<CatalogPage> {
  const accountPath = `/accounts/${encodeURIComponent(accountId)}`;
  switch (catalog) {
    case 'knowledge': {
      const response = await kbApi.listFiles();
      if (!Array.isArray(response?.files)) throw new Error('KB 목록 응답을 확인할 수 없습니다.');
      return { items: response.files.slice(0, MAX_REFERENCE_ITEMS + 1).map(file => reference({
        id: `knowledge:${userId || 'unknown'}:${file.fileId}`, kind: 'knowledge', title: file.fileName,
        href: userId ? `/kb?fileId=${encodeURIComponent(file.fileId)}&ownerId=${encodeURIComponent(userId)}` : undefined,
        caveats: ['파일 목록 정보 · 본문 미확인'],
      })) };
    }
    case 'documents':
    case 'accountDocuments': {
      const response = catalog === 'accountDocuments'
        ? await accountApi.listDocuments(accountId)
        : await docApi.list();
      if (!Array.isArray(response?.documents)) throw new Error('문서 목록 응답을 확인할 수 없습니다.');
      return { items: response.documents.slice(0, MAX_REFERENCE_ITEMS + 1).map(doc => reference({
        id: `document:${catalog === 'accountDocuments' ? accountId : doc.sourceUserId}:${doc.docId}`,
        kind: 'document', title: doc.title,
        href: catalog === 'accountDocuments'
          ? `${accountPath}/docs/${encodeURIComponent(doc.docId)}`
          : `/docs/${encodeURIComponent(doc.docId)}`,
        caveats: ['목록 정보 · 본문 미확인', ...(doc.sharedBy ? ['공유받은 읽기 전용 문서'] : [])],
      })) };
    }
    case 'meetings': {
      const response = await meetingsApi.list({ accountId: accountId || undefined, limit: PAGE_SIZE, cursor });
      if (!Array.isArray(response?.meetings)) throw new Error('회의 목록 응답을 확인할 수 없습니다.');
      return {
        items: response.meetings.slice(0, MAX_REFERENCE_ITEMS + 1).map(meeting => reference({
          id: `meeting:${meeting.meetingId}`, kind: 'meeting', title: meeting.title,
          href: `/meeting/${encodeURIComponent(meeting.meetingId)}`,
          excerpt: meeting.summary,
          caveats: [meeting.date || meeting.createdAt, '목록 요약 · 전체 회의록 미확인'].filter(Boolean),
        })),
        next: response.nextCursor || undefined,
      };
    }
    case 'insight': {
      const response = await accountApi.insights(accountId);
      if (!Array.isArray(response?.insights)) throw new Error('고객 인사이트 응답을 확인할 수 없습니다.');
      return { items: response.insights.slice(0, MAX_REFERENCE_ITEMS + 1).map((insight, index) => reference({
        id: `insight:${accountId}:${insight.sourceId}:${insight.occurredAt}:${index}`,
        kind: 'insight', title: `${insight.type} · ${insight.occurredAt?.slice(0, 10) || '날짜 없음'}`,
        href: insight.sourceId && insight.sourceType === 'meeting'
          ? `/meeting/${encodeURIComponent(insight.sourceId)}`
          : insight.sourceId && insight.sourceType === 'research'
            ? `/insights/research/${encodeURIComponent(insight.sourceId)}`
            : accountPath,
        excerpt: insight.text,
        caveats: ['이전에 추출된 인사이트 · 현재 원문 확인 필요'],
      })) };
    }
    case 'research': {
      const response = await accountApi.research(accountId);
      if (!Array.isArray(response?.research)) throw new Error('고객 리서치 응답을 확인할 수 없습니다.');
      return { items: response.research.slice(0, MAX_REFERENCE_ITEMS + 1).map(research => reference({
        id: `research:${research.researchId}`, kind: 'research', title: research.topic,
        href: `/insights/research/${encodeURIComponent(research.researchId)}`,
        excerpt: research.summary,
        caveats: ['목록 요약 · 전체 리서치 미확인', ...(research.status ? [`상태: ${research.status}`] : [])],
      })) };
    }
    case 'news': {
      const page = Number(cursor || 1);
      const response = await insightsApi.list({ type: 'news', page, limit: PAGE_SIZE });
      if (!Array.isArray(response?.documents)) throw new Error('수집된 Insights 응답을 확인할 수 없습니다.');
      return {
        items: response.documents.slice(0, MAX_REFERENCE_ITEMS + 1).map(doc => reference({
          id: `news:${doc.sourceId || ''}:${doc.docHash}`, kind: 'news', title: doc.title,
          href: doc.sourceId && doc.docHash
            ? `/insights/${encodeURIComponent(doc.sourceId)}/${encodeURIComponent(doc.docHash)}`
            : undefined,
          excerpt: doc.summary,
          caveats: ['기존 수집 자료 · 요약 발췌'],
        })),
        next: page * PAGE_SIZE < response.totalCount ? String(page + 1) : undefined,
      };
    }
  }
}

interface CatalogState extends CatalogPage {
  loading: boolean;
  loaded: boolean;
  error?: string;
  limited: boolean;
}

export function useReferenceCatalog(catalog: ReferenceCatalog, accountId: string, userId?: string) {
  const [state, setState] = useState<CatalogState>({ items: [], loading: true, loaded: false, limited: false });
  const generation = useRef(0);
  const busy = useRef(false);
  const failedCursor = useRef<string | undefined>(undefined);

  const fetchPage = useCallback(async (cursor?: string) => {
    const request = ++generation.current;
    busy.current = true;
    try {
      const page = await readPage(catalog, accountId, userId, cursor);
      if (request !== generation.current) return;
      failedCursor.current = undefined;
      setState(previous => {
        const all = [...(cursor ? previous.items : []), ...page.items];
        const unique = [...new Map(all.map(item => [item.id, item])).values()];
        const limited = unique.length > MAX_REFERENCE_ITEMS || (unique.length >= MAX_REFERENCE_ITEMS && !!page.next);
        return {
          items: unique.slice(0, MAX_REFERENCE_ITEMS), next: limited ? undefined : page.next,
          limited, loaded: true, loading: false,
        };
      });
    } catch (error) {
      if (request !== generation.current) return;
      failedCursor.current = cursor;
      setState(previous => ({
        ...previous, loading: false,
        error: error instanceof Error ? error.message : '자료를 불러오지 못했습니다.',
      }));
    } finally {
      if (request === generation.current) busy.current = false;
    }
  }, [catalog, accountId, userId]);

  useEffect(() => {
    void fetchPage();
    return () => { generation.current += 1; busy.current = false; };
  }, [fetchPage]);

  const loadMore = () => {
    if (busy.current || !state.next) return;
    setState(previous => ({ ...previous, loading: true, error: undefined }));
    void fetchPage(state.next);
  };
  const retry = () => {
    if (busy.current) return;
    setState(previous => ({ ...previous, loading: true, error: undefined }));
    void fetchPage(failedCursor.current);
  };
  return { ...state, loadMore, retry };
}

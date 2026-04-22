'use client';

import { getIdToken, refreshSession } from './auth';
import { triggerAuthFailure } from '@/components/auth/AuthProvider';
import type { CrawlerSourceResponse, CrawledDocument, CrawlHistory, Research, ResearchDetail } from '@/types/meeting';

const API_BASE_URL = process.env.NEXT_PUBLIC_API_URL || '';

interface FetchOptions extends RequestInit {
  skipAuth?: boolean;
}

function isTokenExpired(token: string): boolean {
  try {
    const payload = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')));
    // Expired if within 60s of expiry
    return payload.exp * 1000 < Date.now() + 60_000;
  } catch {
    return true;
  }
}

// No hard redirect — let callers handle auth errors gracefully
// (hard redirect during recording would lose in-progress work)

// Mutex for token refresh — prevents concurrent refreshSession() race conditions
let refreshPromise: Promise<string | null> | null = null;

function refreshTokenOnce(): Promise<string | null> {
  if (refreshPromise) return refreshPromise;
  refreshPromise = refreshSession().finally(() => { refreshPromise = null; });
  return refreshPromise;
}

async function getAuthHeaders(): Promise<Record<string, string>> {
  let token = getIdToken();

  if (!token || isTokenExpired(token)) {
    token = await refreshTokenOnce();
  }

  if (!token) {
    return {};
  }

  return {
    Authorization: `Bearer ${token}`,
  };
}

export async function apiFetch<T>(
  endpoint: string,
  options: FetchOptions = {}
): Promise<T> {
  const { skipAuth = false, headers = {}, ...rest } = options;

  const authHeaders = skipAuth ? {} : await getAuthHeaders();

  const url = `${API_BASE_URL}${endpoint}`;
  const mergedHeaders = {
    'Content-Type': 'application/json',
    ...authHeaders,
    ...headers,
  };

  let response = await fetch(url, { ...rest, headers: mergedHeaders });

  // On 401, refresh token once and retry (mutex prevents concurrent refresh races)
  if (response.status === 401 && !skipAuth) {
    const freshToken = await refreshTokenOnce();
    if (!freshToken) {
      triggerAuthFailure();
      throw new Error('Authentication required');
    }
    response = await fetch(url, {
      ...rest,
      headers: { ...mergedHeaders, Authorization: `Bearer ${freshToken}` },
    });
    if (response.status === 401) {
      triggerAuthFailure();
      throw new Error('Authentication required');
    }
  }

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: { code: 'UNKNOWN', message: 'Request failed' } }));
    throw new Error(errorData.error?.message || `HTTP ${response.status}`);
  }

  if (response.status === 204 || response.headers.get('content-length') === '0') {
    return undefined as T;
  }

  const contentType = response.headers.get('content-type') || '';
  if (!contentType.includes('application/json')) {
    throw new Error(`Unexpected response type: ${contentType || 'unknown'}`);
  }

  return response.json();
}

export const api = {
  get: <T>(endpoint: string, options?: FetchOptions) =>
    apiFetch<T>(endpoint, { ...options, method: 'GET' }),

  post: <T>(endpoint: string, data?: unknown, options?: FetchOptions) =>
    apiFetch<T>(endpoint, {
      ...options,
      method: 'POST',
      body: data ? JSON.stringify(data) : undefined,
    }),

  put: <T>(endpoint: string, data?: unknown, options?: FetchOptions) =>
    apiFetch<T>(endpoint, {
      ...options,
      method: 'PUT',
      body: data ? JSON.stringify(data) : undefined,
    }),

  patch: <T>(endpoint: string, data?: unknown, options?: FetchOptions) =>
    apiFetch<T>(endpoint, {
      ...options,
      method: 'PATCH',
      body: data ? JSON.stringify(data) : undefined,
    }),

  delete: <T>(endpoint: string, options?: FetchOptions) =>
    apiFetch<T>(endpoint, { ...options, method: 'DELETE' }),
};

// Meeting API endpoints
export const meetingsApi = {
  list: (params?: { tab?: 'all' | 'shared'; cursor?: string; limit?: number }) => {
    const query = new URLSearchParams();
    if (params?.tab) query.set('tab', params.tab);
    if (params?.cursor) query.set('cursor', params.cursor);
    if (params?.limit) query.set('limit', params.limit.toString());
    const queryStr = query.toString();
    return api.get<{ meetings: import('@/types/meeting').Meeting[]; nextCursor: string | null }>(
      `/api/meetings${queryStr ? `?${queryStr}` : ''}`
    );
  },

  get: (id: string) => api.get<import('@/types/meeting').Meeting>(`/api/meetings/${id}`),

  create: (data: { title: string; date?: string; participants?: string[]; sttProvider?: 'transcribe' | 'nova-sonic'; status?: string }) =>
    api.post<import('@/types/meeting').Meeting>('/api/meetings', data),

  recover: (meetingId: string) =>
    api.post<{ meetingId: string; status: string }>(`/api/meetings/${meetingId}/recover`, {}),

  update: (id: string, data: { title?: string; content?: string; notes?: string; transcriptA?: string; selectedTranscript?: 'A' | 'B'; participants?: string[]; status?: string }) =>
    api.put<{ meetingId: string; updatedAt: string }>(`/api/meetings/${id}`, data),

  delete: (id: string) => api.delete(`/api/meetings/${id}`),

  share: (id: string, data: { email: string; permission: 'read' | 'edit' }) =>
    api.post<{ sharedWith: { userId: string; email: string; permission: string } }>(`/api/meetings/${id}/share`, data),

  unshare: (id: string, userId: string) =>
    api.delete(`/api/meetings/${id}/share/${userId}`),

  selectTranscript: (id: string, selected: 'A' | 'B') =>
    api.put(`/api/meetings/${id}/transcript`, { selected }),

  updateSpeakers: (id: string, speakerMap: Record<string, string>) =>
    api.put<{ meetingId: string; updatedAt: string }>(`/api/meetings/${id}/speakers`, { speakerMap }),

  audioUrl: (id: string) =>
    api.get<{ audioUrl: string }>(`/api/meetings/${id}/audio`),
};

// Presigned URL for uploads
export const uploadsApi = {
  getPresignedUrl: (data: { fileName: string; fileType: string; category: 'audio' | 'image' | 'file'; meetingId?: string }) =>
    api.post<{ uploadUrl: string; key: string; expiresIn: number }>('/api/upload/presigned', data),

  notifyComplete: (data: { meetingId: string; key: string; category: 'audio' | 'image' | 'file'; fileName?: string; fileSize?: number; mimeType?: string }) =>
    api.post<{ status: string }>('/api/upload/complete', data),
};

// User search for sharing
export const usersApi = {
  search: (query: string) =>
    api.get<{ users: import('@/types/meeting').User[] }>(`/api/users/search?q=${encodeURIComponent(query)}`),
};

// KB API
export const kbApi = {
  upload: (data: { fileName: string; fileType: string }) =>
    api.post<{ uploadUrl: string; key: string; expiresIn: number }>('/api/kb/upload', data),

  sync: () => api.post<{ status: string }>('/api/kb/sync'),

  listFiles: () =>
    api.get<{ files: import('@/types/meeting').KBFile[] }>('/api/kb/files'),

  deleteFile: (fileId: string) => api.delete(`/api/kb/files/${fileId}`),

  copyAttachment: (sourceKey: string) =>
    api.post<{ status: string; ingestion: string }>('/api/kb/copy-attachment', { sourceKey }),
};

// Q&A API
interface QAResponse {
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
}

export const qaApi = {
  ask: (question: string, context?: string, sessionId?: string) =>
    api.post<QAResponse>(
      '/api/qa/ask',
      { question, context, sessionId }
    ),

  askMeeting: (meetingId: string, question: string, sessionId?: string) =>
    api.post<QAResponse>(
      `/api/qa/meeting/${meetingId}`,
      { question, sessionId }
    ),

  detectQuestions: (transcript: string, previousQuestions?: string[], summary?: string) =>
    api.post<{ questions: string[] }>(
      '/api/qa/detect-questions',
      { transcript, previousQuestions, summary }
    ),
};

// Export API
export const exportApi = {
  export: (meetingId: string, format: 'pdf' | 'notion' | 'obsidian') =>
    api.post<import('@/types/meeting').ExportResponse>(
      `/api/meetings/${meetingId}/export`,
      { format }
    ),

  obsidian: (meetingId: string) =>
    api.get<{ filename: string; content: string }>(
      `/api/meetings/${meetingId}/export/obsidian`
    ),
};

// Settings API
export const settingsApi = {
  getIntegrations: () =>
    api.get<import('@/types/meeting').IntegrationsResponse>('/api/settings/integrations'),

  saveNotionKey: (apiKey: string) =>
    api.put<{ status: string }>('/api/settings/integrations/notion', { apiKey }),

  deleteNotionKey: () => api.delete('/api/settings/integrations/notion'),
};

// Translation API
export const translateApi = {
  translate: (text: string, sourceLang: string, targetLang: string) =>
    api.post<{ translatedText: string; sourceLang: string; targetLang: string }>(
      '/api/translate',
      { text, sourceLang, targetLang }
    ),
};

// Live Summary API
export const summaryApi = {
  summarizeLive: (meetingId: string, transcript: string, previousSummary?: string) =>
    api.post<{ summary: string }>(
      `/api/meetings/${meetingId}/summarize`,
      { transcript, previousSummary }
    ),
};

// Crawler API
export const crawlerApi = {
  listSources: () =>
    api.get<{ sources: CrawlerSourceResponse[] }>('/api/crawler/sources'),
  addSource: (data: {
    sourceName: string;
    awsServices: string[];
    newsSources: string[];
    customUrls?: string[];
    newsQueries?: string[];
  }) => api.post<CrawlerSourceResponse>('/api/crawler/sources', data),
  updateSource: (sourceId: string, data: {
    awsServices: string[];
    newsSources: string[];
    customUrls?: string[];
  }) => api.put<{ status: string }>(`/api/crawler/sources/${sourceId}`, data),
  unsubscribe: (sourceId: string) =>
    api.delete(`/api/crawler/sources/${sourceId}`),
  getHistory: (sourceId: string) =>
    api.get<{ history: CrawlHistory[] }>(`/api/crawler/sources/${sourceId}/history`),
};

// Insights API
export const insightsApi = {
  list: (params: { type: string; source?: string; service?: string; page?: number; limit?: number }) => {
    const q = new URLSearchParams();
    q.set('type', params.type);
    if (params.source) q.set('source', params.source);
    if (params.service) q.set('service', params.service);
    q.set('page', String(params.page || 1));
    q.set('limit', String(params.limit || 20));
    return api.get<{
      documents: CrawledDocument[];
      totalCount: number;
      page: number;
      limit: number;
    }>(`/api/insights?${q.toString()}`);
  },
  getDetail: (sourceId: string, docHash: string) =>
    api.get<CrawledDocument & { content: string }>(`/api/insights/${encodeURIComponent(sourceId)}/${encodeURIComponent(docHash)}`),
};

// Research API
export const researchApi = {
  create: (data: { topic: string; mode: string }) =>
    api.post<Research>('/api/research', data),
  list: () =>
    api.get<{ research: Research[] }>('/api/research'),
  getDetail: (researchId: string) =>
    api.get<ResearchDetail>(`/api/research/${encodeURIComponent(researchId)}`),
  delete: (researchId: string) =>
    api.delete(`/api/research/${encodeURIComponent(researchId)}`),
};


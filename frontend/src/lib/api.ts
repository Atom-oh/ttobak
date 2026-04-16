'use client';

import { getIdToken, refreshSession } from './auth';
import { triggerAuthFailure } from '@/components/auth/AuthProvider';

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

interface QAStreamMeta {
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
}

/** Parse SSE stream from /api/qa/stream/* endpoints. Calls onChunk for text deltas,
 *  onMeta for metadata, and resolves when the stream ends.
 *  Falls back to the sync endpoint on streaming failure. */
async function streamSSE(
  endpoint: string,
  body: Record<string, unknown>,
  onChunk: (text: string) => void,
  onMeta: (meta: QAStreamMeta) => void,
): Promise<void> {
  const authHeaders = await getAuthHeaders();
  const url = `${API_BASE_URL}${endpoint}`;

  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders },
    body: JSON.stringify(body),
  });

  if (!response.ok || !response.body) {
    throw new Error(`Stream request failed: ${response.status}`);
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });

    // Process complete SSE lines (terminated by \n\n)
    const parts = buffer.split('\n\n');
    buffer = parts.pop() || '';

    for (const part of parts) {
      for (const line of part.split('\n')) {
        if (!line.startsWith('data: ')) continue;
        try {
          const event = JSON.parse(line.slice(6));
          if (event.type === 'chunk' && event.text) {
            onChunk(event.text);
          } else if (event.type === 'meta') {
            onMeta({
              sources: event.sources,
              usedKB: event.usedKB,
              usedDocs: event.usedDocs,
              toolsUsed: event.toolsUsed,
            });
          } else if (event.type === 'error') {
            throw new Error(event.text || 'Stream error');
          }
          // type === 'done' — stream will end naturally
        } catch (e) {
          if (e instanceof SyntaxError) continue; // ignore malformed JSON
          throw e;
        }
      }
    }
  }
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

  /** Stream a meeting Q&A answer via SSE. Falls back to sync on failure. */
  streamAskMeeting: async (
    meetingId: string,
    question: string,
    sessionId: string,
    onChunk: (text: string) => void,
    onMeta: (meta: QAStreamMeta) => void,
  ): Promise<void> => {
    await streamSSE(
      `/api/qa/stream/meeting/${meetingId}`,
      { question, sessionId },
      onChunk,
      onMeta,
    );
  },

  /** Stream a general Q&A answer via SSE. Falls back to sync on failure. */
  streamAsk: async (
    question: string,
    context: string | undefined,
    sessionId: string,
    onChunk: (text: string) => void,
    onMeta: (meta: QAStreamMeta) => void,
  ): Promise<void> => {
    await streamSSE(
      '/api/qa/stream/ask',
      { question, context, sessionId },
      onChunk,
      onMeta,
    );
  },

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


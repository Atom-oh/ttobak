'use client';

import { getIdToken, refreshSession } from './auth';

const API_BASE_URL = process.env.NEXT_PUBLIC_API_URL || '';

interface FetchOptions extends RequestInit {
  skipAuth?: boolean;
}

async function getAuthHeaders(): Promise<Record<string, string>> {
  let token = getIdToken();

  if (!token) {
    token = await refreshSession();
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

  const response = await fetch(`${API_BASE_URL}${endpoint}`, {
    ...rest,
    headers: {
      'Content-Type': 'application/json',
      ...authHeaders,
      ...headers,
    },
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: { code: 'UNKNOWN', message: 'Request failed' } }));
    throw new Error(errorData.error?.message || `HTTP ${response.status}`);
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

  create: (data: { title: string; date?: string; participants?: string[] }) =>
    api.post<import('@/types/meeting').Meeting>('/api/meetings', data),

  update: (id: string, data: { title?: string; content?: string; selectedTranscript?: 'A' | 'B'; participants?: string[]; status?: string }) =>
    api.put<{ meetingId: string; updatedAt: string }>(`/api/meetings/${id}`, data),

  delete: (id: string) => api.delete(`/api/meetings/${id}`),

  share: (id: string, data: { email: string; permission: 'read' | 'edit' }) =>
    api.post<{ sharedWith: { userId: string; email: string; permission: string } }>(`/api/meetings/${id}/share`, data),

  unshare: (id: string, userId: string) =>
    api.delete(`/api/meetings/${id}/share/${userId}`),

  selectTranscript: (id: string, selected: 'A' | 'B') =>
    api.put(`/api/meetings/${id}/transcript`, { selected }),
};

// Presigned URL for uploads
export const uploadsApi = {
  getPresignedUrl: (data: { fileName: string; fileType: string; category: 'audio' | 'image' }) =>
    api.post<{ uploadUrl: string; key: string; expiresIn: number }>('/api/upload/presigned', data),

  notifyComplete: (data: { meetingId: string; key: string; category: 'audio' | 'image' }) =>
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
};

// Q&A API
export const qaApi = {
  ask: (meetingId: string, question: string) =>
    api.post<{ answer: string; sources?: { title: string; snippet: string }[] }>(
      `/api/meetings/${meetingId}/ask`,
      { question }
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

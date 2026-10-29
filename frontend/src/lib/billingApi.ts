'use client';

import { ApiError, getAuthHeaders, refreshTokenOnce } from '@/lib/api';
import type { BillingAccountEntry, BillingDatasetId } from '@/lib/billingSummary';

const DATASET_IDS: ReadonlySet<string> = new Set<BillingDatasetId>(['kpis', 'period', 'briefing', 'sessions', 'tag_signals', 'cur_tag_signals']);

/**
 * Same-origin Billing BFF reads (/api/billing/...), never API_BASE_URL. Bearer
 * token and one refresh-and-retry on 401 as apiFetch, but a Billing 401 never
 * signs the user out of the app (no global auth failure): it is ApiError(401,
 * 'BILLING') and the Billing UI hides or shows its error state. The BFF's flat
 * `{ error: string }` bodies become ApiError(status, 'BILLING', message).
 */
export async function billingFetch<T>(path: string, signal?: AbortSignal): Promise<T> {
  if (!path.startsWith('/') || path.startsWith('//') || path.includes('..') || path.includes('\\')) {
    throw new Error('Billing path must be relative to /api/billing');
  }
  const url = `/api/billing${path}`;
  const authHeaders = await getAuthHeaders();
  const request = (headers: Record<string, string>) => fetch(url, { headers, signal, cache: 'no-store', credentials: 'same-origin' });

  let response = await request(authHeaders);
  if (response.status === 401) {
    const freshToken = await refreshTokenOnce();
    if (!freshToken) throw new ApiError(401, 'BILLING', 'Billing authentication failed');
    response = await request({ ...authHeaders, Authorization: `Bearer ${freshToken}` });
    if (response.status === 401) throw new ApiError(401, 'BILLING', 'Billing authentication failed');
  }

  if (!response.ok) {
    const body = await response.json().catch(() => null);
    const message = body && typeof body.error === 'string' ? body.error : `HTTP ${response.status}`;
    throw new ApiError(response.status, 'BILLING', message);
  }
  return response.json() as Promise<T>;
}

export async function fetchBillingAccounts(signal?: AbortSignal): Promise<BillingAccountEntry[]> {
  const accounts = await billingFetch<unknown>('/accounts', signal);
  if (!Array.isArray(accounts)) throw new ApiError(502, 'BILLING', 'Unexpected Billing accounts response');
  return accounts as BillingAccountEntry[];
}

/** Current rows of one dataset for one Account (`?acct=`; the BFF answers 404 for non-members). */
export async function fetchAccountDataset<T = unknown>(id: BillingDatasetId, acct: string, signal?: AbortSignal): Promise<T[]> {
  if (!DATASET_IDS.has(id)) throw new Error(`Unknown Billing dataset: ${id}`);
  const rows = await billingFetch<unknown>(`/data/${id}.json?${new URLSearchParams({ acct })}`, signal);
  if (!Array.isArray(rows)) throw new ApiError(502, 'BILLING', 'Unexpected Billing dataset response');
  return rows as T[];
}

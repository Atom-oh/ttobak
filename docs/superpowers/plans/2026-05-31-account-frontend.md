# Account Frontend UI — Implementation Plan (Plan 6 of 6)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development or superpowers:executing-plans. Steps use `- [ ]` checkboxes.

**Goal:** Account를 다루는 최소 UI: Account 목록/등록, 멤버 초대, Account 상세(멤버·공유미팅·인사이트(유형 필터)·문서 열람), 미팅 상세에 Account 연결·공유 토글. 비-Obsidian 팀원이 TTOBAK 웹에서 Account 원재료를 본다.

**Architecture:** Next.js 16 App Router 정적 export. `src/lib/api.ts`에 `accountApi`/`meetingAccountApi` 추가, `src/types/meeting.ts`에 타입. `/accounts`(목록+생성), `/accounts/[id]`(server page `generateStaticParams [{id:'_'}]` + `AccountDetailClient` — id는 `usePathname()`로 런타임 추출). 기존 `KBFileList`/`IntegrationSettings`/auth-guard 패턴 미러. **테스트 프레임워크 없음 → 각 Task 검증은 `npm run lint` + `npm run build`.**

**Tech Stack:** Next.js 16.1.6 / React 19 / Tailwind v4(class dark mode, `--primary #3211d4`) / TS strict(no `any`, `import type`, `err instanceof Error`). Material Symbols.

**선행:** Plan 1-5(백엔드/MCP) 완료, branch `feat/account-foundation`.

## File Structure (Plan 6)
| 파일 | 변경 |
|---|---|
| `frontend/src/types/meeting.ts` | Account 관련 인터페이스 |
| `frontend/src/lib/api.ts` | `accountApi` + `meetingAccountApi` |
| `frontend/src/components/layout/Sidebar.tsx` | Accounts nav 항목 |
| `frontend/src/app/accounts/page.tsx` | 목록+생성 페이지 (신규) |
| `frontend/src/components/AccountsClient.tsx` | 목록+생성 폼 (신규) |
| `frontend/src/app/accounts/[id]/page.tsx` | server page (신규) |
| `frontend/src/components/AccountDetailClient.tsx` | Account 상세 (신규) |
| `frontend/src/components/meeting/AccountSection.tsx` | 미팅↔Account 연결·공유 (신규) |
| `frontend/src/components/MeetingDetailClient.tsx` | AccountSection 삽입 |

> 검증: 모든 Task는 `cd frontend && npm run lint && npm run build`(prod 정적 export)로 확인. 배포는 하지 않음(사용자 요청 시에만).

---

## Task 1: 타입 추가

**Files:** Modify `frontend/src/types/meeting.ts`

- [ ] **Step 1: 파일 끝에 추가**

```ts
export interface AccountSummary {
  accountId: string;
  name: string;
  role: string;
}

export interface AccountMember {
  userId: string;
  email?: string;
  role: string;
}

export interface Account {
  accountId: string;
  name: string;
  aliases?: string[];
  domains?: string[];
  industry?: string;
  ownerUserId: string;
  members: AccountMember[];
  createdAt: string;
}

export interface AccountMeetingRef {
  meetingId: string;
  ownerUserId: string;
  title: string;
  date: string;
}

export interface AccountInsight {
  type: string;
  text: string;
  sourceType: string;
  sourceId: string;
  occurredAt: string;
  tsMarker?: string;
  entities?: string[];
}

export interface AccountDocument {
  docId: string;
  title: string;
  docType?: string;
  path?: string;
  sourceUserId: string;
  createdAt: string;
  content?: string;
}

export const INSIGHT_TYPES = [
  'trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action',
] as const;
```

- [ ] **Step 2: 검증** `cd frontend && npm run lint && npm run build` → 성공.
- [ ] **Step 3: Commit**
```bash
cd frontend && git add src/types/meeting.ts
git commit -m "feat(account-ui): account types"
```

---

## Task 2: API 클라이언트

**Files:** Modify `frontend/src/lib/api.ts`

- [ ] **Step 1: import 타입 추가** — 파일 상단 `@/types/meeting` import에 `Account, AccountSummary, AccountMember, AccountMeetingRef, AccountInsight, AccountDocument` 추가(기존 import 그룹에 합치거나 inline generic 사용). 그리고 `accountApi`/`meetingAccountApi`를 다른 `*Api` 객체들 옆(파일 하단)에 추가:

```ts
export const accountApi = {
  list: () => api.get<{ accounts: AccountSummary[] }>('/api/accounts'),
  get: (id: string) => api.get<Account>(`/api/accounts/${encodeURIComponent(id)}`),
  create: (data: { name: string; aliases?: string[]; domains?: string[]; industry?: string }) =>
    api.post<Account>('/api/accounts', data),
  addMember: (id: string, data: { email: string; role: string }) =>
    api.post<AccountMember>(`/api/accounts/${encodeURIComponent(id)}/members`, data),
  meetings: (id: string) =>
    api.get<{ meetings: AccountMeetingRef[] }>(`/api/accounts/${encodeURIComponent(id)}/meetings`),
  insights: (id: string, params?: { from?: string; to?: string; types?: string[] }) => {
    const q = new URLSearchParams();
    if (params?.from) q.set('from', params.from);
    if (params?.to) q.set('to', params.to);
    if (params?.types?.length) q.set('types', params.types.join(','));
    const qs = q.toString();
    return api.get<{ insights: AccountInsight[] }>(
      `/api/accounts/${encodeURIComponent(id)}/insights${qs ? `?${qs}` : ''}`);
  },
  listDocuments: (id: string, docType?: string) => {
    const q = new URLSearchParams();
    if (docType) q.set('docType', docType);
    const qs = q.toString();
    return api.get<{ documents: AccountDocument[] }>(
      `/api/accounts/${encodeURIComponent(id)}/documents${qs ? `?${qs}` : ''}`);
  },
  getDocument: (id: string, docId: string) =>
    api.get<AccountDocument>(
      `/api/accounts/${encodeURIComponent(id)}/documents/${encodeURIComponent(docId)}`),
};

export const meetingAccountApi = {
  link: (meetingId: string, accountId: string) =>
    api.post<{ accountId: string }>(`/api/meetings/${encodeURIComponent(meetingId)}/account`, { accountId }),
  shareToAccount: (meetingId: string, accountId: string) =>
    api.post<{ accountId: string; sharedWith: number }>(
      `/api/meetings/${encodeURIComponent(meetingId)}/share-account`, { accountId }),
};
```

- [ ] **Step 2: 검증** `cd frontend && npm run lint && npm run build` → 성공.
- [ ] **Step 3: Commit**
```bash
cd frontend && git add src/lib/api.ts
git commit -m "feat(account-ui): accountApi + meetingAccountApi client"
```

---

## Task 3: 사이드바 Accounts 항목

**Files:** Modify `frontend/src/components/layout/Sidebar.tsx`

- [ ] **Step 1:** `mainNav` 배열에서 `Meetings` 다음에 추가:

```ts
  { href: '/accounts', icon: 'corporate_fare', label: 'Accounts' },
```

- [ ] **Step 2: 검증** `cd frontend && npm run lint && npm run build` → 성공.
- [ ] **Step 3: Commit**
```bash
cd frontend && git add src/components/layout/Sidebar.tsx
git commit -m "feat(account-ui): Accounts sidebar nav entry"
```

---

## Task 4: Accounts 목록 + 등록 페이지

**Files:** create `frontend/src/components/AccountsClient.tsx`, `frontend/src/app/accounts/page.tsx`

- [ ] **Step 1: AccountsClient 컴포넌트** (`src/components/AccountsClient.tsx`)

```tsx
'use client';

import { useCallback, useEffect, useState } from 'react';
import { useRouter } from 'next/navigation';
import { accountApi } from '@/lib/api';
import type { AccountSummary } from '@/types/meeting';

export default function AccountsClient() {
  const router = useRouter();
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState('');
  const [aliases, setAliases] = useState('');
  const [industry, setIndustry] = useState('');
  const [creating, setCreating] = useState(false);

  const fetchAccounts = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await accountApi.list();
      setAccounts(res?.accounts ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load accounts');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchAccounts();
  }, [fetchAccounts]);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setCreating(true);
    setError(null);
    try {
      const created = await accountApi.create({
        name: name.trim(),
        aliases: aliases.split(',').map((s) => s.trim()).filter(Boolean),
        industry: industry.trim() || undefined,
      });
      setShowForm(false);
      setName('');
      setAliases('');
      setIndustry('');
      router.push(`/accounts/${created.accountId}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create account');
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className="max-w-3xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <h2 className="text-xl font-bold text-slate-900 dark:text-[#e4e1e9]">Accounts</h2>
        <button
          onClick={() => setShowForm((v) => !v)}
          className="bg-primary hover:bg-primary-hover text-white dark:text-[#09090E] rounded-lg font-semibold text-sm px-4 py-2 flex items-center gap-1"
        >
          <span className="material-symbols-outlined text-lg">add</span>New Account
        </button>
      </div>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
          {error}
        </div>
      )}

      {showForm && (
        <form onSubmit={handleCreate} className="glass-panel rounded-xl p-5 mb-6 space-y-3">
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Account name (e.g. 하나은행)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            value={aliases}
            onChange={(e) => setAliases(e.target.value)}
            placeholder="Aliases / tags, comma-separated (하나은행, Hana Bank)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <input
            value={industry}
            onChange={(e) => setIndustry(e.target.value)}
            placeholder="Industry (optional)"
            className="w-full px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
          />
          <button
            type="submit"
            disabled={creating || !name.trim()}
            className="bg-primary hover:bg-primary-hover text-white dark:text-[#09090E] rounded-lg font-semibold text-sm px-4 py-2 disabled:opacity-50"
          >
            {creating ? 'Creating…' : 'Create'}
          </button>
        </form>
      )}

      {loading ? (
        <div className="flex items-center justify-center py-16">
          <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
        </div>
      ) : accounts.length === 0 ? (
        <div className="text-center py-16 text-slate-400 dark:text-[#849396]">
          <span className="material-symbols-outlined text-4xl mb-2 block">corporate_fare</span>
          No accounts yet. Create one to start organizing customers.
        </div>
      ) : (
        <div className="bg-white rounded-xl border border-slate-200 divide-y divide-slate-200 dark:glass-panel dark:divide-white/5">
          {accounts.map((a) => (
            <button
              key={a.accountId}
              onClick={() => router.push(`/accounts/${a.accountId}`)}
              className="w-full flex items-center justify-between p-4 text-left hover:bg-slate-50 dark:hover:bg-white/5"
            >
              <div className="flex items-center gap-3">
                <span className="material-symbols-outlined text-primary">corporate_fare</span>
                <span className="font-medium text-slate-900 dark:text-[#e4e1e9]">{a.name}</span>
              </div>
              <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">
                {a.role}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 2: 페이지** (`src/app/accounts/page.tsx`) — `kb/page.tsx` 가드 패턴 미러

```tsx
'use client';

import { useAuth } from '@/components/auth/AuthProvider';
import AppLayout from '@/components/layout/AppLayout';
import AccountsClient from '@/components/AccountsClient';

export default function AccountsPage() {
  const { isLoading, isAuthenticated } = useAuth();
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) {
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }
  return (
    <AppLayout activePath="/accounts">
      <div className="p-4 lg:p-8">
        <AccountsClient />
      </div>
    </AppLayout>
  );
}
```

> `AppLayout`/`useAuth`의 정확한 import 경로·props는 `src/app/kb/page.tsx`를 열어 확인하고 맞춘다(activePath prop 형태 포함).

- [ ] **Step 3: 검증** `cd frontend && npm run lint && npm run build` → 성공.
- [ ] **Step 4: Commit**
```bash
cd frontend && git add src/components/AccountsClient.tsx src/app/accounts/page.tsx
git commit -m "feat(account-ui): accounts list + create page"
```

---

## Task 5: Account 상세 페이지

**Files:** create `frontend/src/app/accounts/[id]/page.tsx`, `frontend/src/components/AccountDetailClient.tsx`

- [ ] **Step 1: server page** (`src/app/accounts/[id]/page.tsx`) — meeting/[id]/page.tsx 패턴

```tsx
import AccountDetailClient from '@/components/AccountDetailClient';

export async function generateStaticParams() {
  return [{ id: '_' }];
}

export default async function Page(props: { params: Promise<{ id: string }> }) {
  await props.params;
  return <AccountDetailClient />;
}
```

- [ ] **Step 2: AccountDetailClient** (`src/components/AccountDetailClient.tsx`)

```tsx
'use client';

import { useCallback, useEffect, useState } from 'react';
import { usePathname } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import AppLayout from '@/components/layout/AppLayout';
import { accountApi } from '@/lib/api';
import { INSIGHT_TYPES } from '@/types/meeting';
import type { Account, AccountInsight, AccountMeetingRef, AccountDocument } from '@/types/meeting';

export default function AccountDetailClient() {
  const pathname = usePathname();
  const accountId = decodeURIComponent(pathname.split('/').filter(Boolean).pop() || '');
  const { isLoading, isAuthenticated } = useAuth();

  const [account, setAccount] = useState<Account | null>(null);
  const [meetings, setMeetings] = useState<AccountMeetingRef[]>([]);
  const [insights, setInsights] = useState<AccountInsight[]>([]);
  const [documents, setDocuments] = useState<AccountDocument[]>([]);
  const [activeType, setActiveType] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState('SSA');
  const [inviting, setInviting] = useState(false);

  const fetchAll = useCallback(async () => {
    if (!accountId || accountId === '_') return;
    setLoading(true);
    setError(null);
    try {
      const [acc, mtg, ins, docs] = await Promise.all([
        accountApi.get(accountId),
        accountApi.meetings(accountId),
        accountApi.insights(accountId),
        accountApi.listDocuments(accountId),
      ]);
      setAccount(acc);
      setMeetings(mtg?.meetings ?? []);
      setInsights(ins?.insights ?? []);
      setDocuments(docs?.documents ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load account');
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    if (isAuthenticated) fetchAll();
  }, [isAuthenticated, fetchAll]);

  const handleInvite = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!inviteEmail.trim()) return;
    setInviting(true);
    setError(null);
    try {
      await accountApi.addMember(accountId, { email: inviteEmail.trim(), role: inviteRole });
      setInviteEmail('');
      await fetchAll();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add member');
    } finally {
      setInviting(false);
    }
  };

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) {
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }

  const shownInsights = activeType ? insights.filter((i) => i.type === activeType) : insights;

  return (
    <AppLayout activePath="/accounts">
      <div className="p-4 lg:p-8 max-w-4xl mx-auto">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg p-3 mb-4">
            {error}
          </div>
        )}
        {loading || !account ? (
          <div className="flex items-center justify-center py-16">
            <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
          </div>
        ) : (
          <>
            <div className="flex items-center gap-3 mb-6">
              <span className="material-symbols-outlined text-primary text-3xl">corporate_fare</span>
              <div>
                <h2 className="text-xl font-bold text-slate-900 dark:text-[#e4e1e9]">{account.name}</h2>
                {account.industry && (
                  <p className="text-sm text-slate-500 dark:text-[#849396]">{account.industry}</p>
                )}
              </div>
            </div>

            {/* Members */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-[#e4e1e9]">Members</h3>
              <div className="glass-panel rounded-xl p-4 space-y-2">
                {account.members.map((m) => (
                  <div key={m.userId} className="flex items-center justify-between text-sm">
                    <span className="text-slate-700 dark:text-[#bac9cc]">{m.email || m.userId}</span>
                    <span className="text-xs font-semibold px-2 py-1 rounded-full bg-primary/10 text-primary">{m.role}</span>
                  </div>
                ))}
                <form onSubmit={handleInvite} className="flex gap-2 pt-2">
                  <input
                    value={inviteEmail}
                    onChange={(e) => setInviteEmail(e.target.value)}
                    placeholder="colleague@company.com"
                    className="flex-1 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
                  />
                  <select
                    value={inviteRole}
                    onChange={(e) => setInviteRole(e.target.value)}
                    className="px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
                  >
                    <option value="AM">AM</option>
                    <option value="TAM">TAM</option>
                    <option value="SSA">SSA</option>
                  </select>
                  <button
                    type="submit"
                    disabled={inviting || !inviteEmail.trim()}
                    className="bg-primary hover:bg-primary-hover text-white dark:text-[#09090E] rounded-lg font-semibold text-sm px-4 disabled:opacity-50"
                  >
                    Add
                  </button>
                </form>
              </div>
            </section>

            {/* Insights */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-[#e4e1e9]">Field Insights</h3>
              <div className="flex flex-wrap gap-2 mb-3">
                <button
                  onClick={() => setActiveType('')}
                  className={`text-xs px-3 py-1 rounded-full border ${activeType === '' ? 'bg-primary text-white dark:text-[#09090E] border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-[#849396]'}`}
                >
                  all
                </button>
                {INSIGHT_TYPES.map((t) => (
                  <button
                    key={t}
                    onClick={() => setActiveType(t)}
                    className={`text-xs px-3 py-1 rounded-full border ${activeType === t ? 'bg-primary text-white dark:text-[#09090E] border-primary' : 'border-slate-200 dark:border-white/10 text-slate-600 dark:text-[#849396]'}`}
                  >
                    {t}
                  </button>
                ))}
              </div>
              {shownInsights.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-[#849396]">No insights yet.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {shownInsights.map((ins, idx) => (
                    <div key={idx} className="p-3">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-primary/10 text-primary">{ins.type}</span>
                        <span className="text-xs text-slate-400 dark:text-[#849396]">
                          {new Date(ins.occurredAt).toLocaleDateString()}
                        </span>
                      </div>
                      <p className="text-sm text-slate-700 dark:text-[#bac9cc]">{ins.text}</p>
                    </div>
                  ))}
                </div>
              )}
            </section>

            {/* Shared meetings */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-[#e4e1e9]">Shared Meetings</h3>
              {meetings.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-[#849396]">No shared meetings.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {meetings.map((m) => (
                    <a key={m.meetingId} href={`/meeting/${m.meetingId}`} className="block p-3 hover:bg-slate-50 dark:hover:bg-white/5">
                      <div className="text-sm font-medium text-slate-900 dark:text-[#e4e1e9]">{m.title || m.meetingId}</div>
                      <div className="text-xs text-slate-400 dark:text-[#849396]">{new Date(m.date).toLocaleDateString()}</div>
                    </a>
                  ))}
                </div>
              )}
            </section>

            {/* Documents */}
            <section className="mb-8">
              <h3 className="text-base font-bold mb-3 text-slate-900 dark:text-[#e4e1e9]">Documents</h3>
              {documents.length === 0 ? (
                <p className="text-sm text-slate-400 dark:text-[#849396]">No documents.</p>
              ) : (
                <div className="glass-panel rounded-xl divide-y divide-slate-200 dark:divide-white/5">
                  {documents.map((d) => (
                    <div key={d.docId} className="flex items-center gap-2 p-3 text-sm">
                      <span className="material-symbols-outlined text-primary text-lg">description</span>
                      <span className="text-slate-700 dark:text-[#bac9cc]">{d.title}</span>
                      {d.docType && <span className="text-xs text-slate-400 dark:text-[#849396]">({d.docType})</span>}
                    </div>
                  ))}
                </div>
              )}
            </section>
          </>
        )}
      </div>
    </AppLayout>
  );
}
```

- [ ] **Step 3: 검증** `cd frontend && npm run lint && npm run build` → 성공.
- [ ] **Step 4: Commit**
```bash
cd frontend && git add src/app/accounts/[id]/page.tsx src/components/AccountDetailClient.tsx
git commit -m "feat(account-ui): account detail (members/insights/meetings/documents)"
```

---

## Task 6: 미팅↔Account 연결·공유 섹션

**Files:** create `frontend/src/components/meeting/AccountSection.tsx`, modify `frontend/src/components/MeetingDetailClient.tsx`

- [ ] **Step 1: AccountSection** (`src/components/meeting/AccountSection.tsx`)

```tsx
'use client';

import { useEffect, useState } from 'react';
import { accountApi, meetingAccountApi } from '@/lib/api';
import type { AccountSummary } from '@/types/meeting';

interface Props {
  meetingId: string;
  initialAccountId?: string;
  initialShared?: boolean;
}

export default function AccountSection({ meetingId, initialAccountId, initialShared }: Props) {
  const [accounts, setAccounts] = useState<AccountSummary[]>([]);
  const [selected, setSelected] = useState(initialAccountId || '');
  const [shared, setShared] = useState(!!initialShared);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    accountApi.list().then((r) => setAccounts(r?.accounts ?? [])).catch(() => {});
  }, []);

  const handleLink = async () => {
    if (!selected) return;
    setBusy(true);
    setError(null);
    setMsg(null);
    try {
      await meetingAccountApi.link(meetingId, selected);
      setMsg('Linked to account (private).');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to link');
    } finally {
      setBusy(false);
    }
  };

  const handleShare = async () => {
    if (!selected) return;
    setBusy(true);
    setError(null);
    setMsg(null);
    try {
      const res = await meetingAccountApi.shareToAccount(meetingId, selected);
      setShared(true);
      setMsg(`Shared to account team (${res.sharedWith} member(s)).`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to share');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="bg-white dark:bg-surface-lowest glass-panel rounded-xl p-5 dark:border dark:border-white/10">
      <div className="flex flex-col sm:flex-row sm:items-center gap-3">
        <select
          value={selected}
          onChange={(e) => setSelected(e.target.value)}
          className="flex-1 px-3 py-2 rounded-lg border border-slate-200 dark:border-white/10 bg-white dark:bg-surface-lowest text-sm"
        >
          <option value="">Select an account…</option>
          {accounts.map((a) => (
            <option key={a.accountId} value={a.accountId}>{a.name}</option>
          ))}
        </select>
        <button
          onClick={handleLink}
          disabled={busy || !selected}
          className="text-primary border border-primary rounded-lg hover:bg-primary/10 text-sm font-semibold px-4 py-2 disabled:opacity-50 dark:border-[#00E5FF]/30 dark:hover:bg-[#00E5FF]/10"
        >
          Link (private)
        </button>
        <button
          onClick={handleShare}
          disabled={busy || !selected}
          className="bg-primary hover:bg-primary-hover text-white dark:text-[#09090E] rounded-lg font-semibold text-sm px-4 py-2 disabled:opacity-50"
        >
          {shared ? 'Re-share to team' : 'Share to team'}
        </button>
      </div>
      {msg && <p className="text-sm text-green-600 dark:text-green-400 mt-2">{msg}</p>}
      {error && <p className="text-sm text-red-600 dark:text-red-400 mt-2">{error}</p>}
    </div>
  );
}
```

- [ ] **Step 2: MeetingDetailClient에 섹션 삽입** — `src/components/MeetingDetailClient.tsx`를 열어, 메인 컬럼에서 기존 섹션 패턴(`<section className="mb-12">` + `<h3 ...>` 아이콘 헤딩)을 따라 "Meeting Notes" 섹션 부근에 추가. import에 `import AccountSection from '@/components/meeting/AccountSection';` 추가. `meeting` 상태에서 `meetingId`를 얻는다(파일이 이미 meeting 객체를 가짐).

```tsx
            <section className="mb-12">
              <h3 className="text-base font-bold flex items-center gap-2 mb-4 dark:text-text-main">
                <span className="material-symbols-outlined text-primary">corporate_fare</span>
                Account
              </h3>
              <AccountSection
                meetingId={meeting.meetingId}
                initialAccountId={meeting.accountId}
                initialShared={meeting.sharedToAccount}
              />
            </section>
```

> `MeetingDetailClient`의 `meeting` 타입(`MeetingDetail`)에 `accountId`/`sharedToAccount`가 없으면, `src/types/meeting.ts`의 해당 인터페이스에 `accountId?: string; sharedToAccount?: boolean;`를 추가(백엔드 GetMeetingDetail이 이미 Meeting 필드를 반환하면 노출됨; 없으면 optional이라 무해). 정확한 삽입 위치/필드는 파일을 열어 기존 섹션 사이에 맞춰 넣는다.

- [ ] **Step 3: 검증** `cd frontend && npm run lint && npm run build` → 성공.
- [ ] **Step 4: Commit**
```bash
cd frontend && git add src/components/meeting/AccountSection.tsx src/components/MeetingDetailClient.tsx src/types/meeting.ts
git commit -m "feat(account-ui): meeting account link + share-to-team section"
```

---

## Task 7: 최종 검증

- [ ] **Step 1:** `cd frontend && npm run lint && npm run build` → 둘 다 성공(정적 export 포함).
- [ ] **Step 2:** (배포 안 함) — 사용자가 요청하면 별도로 `aws s3 sync out/ ...` + CloudFront invalidation.

---

## Self-Review (작성자 체크)
- **Spec 커버리지(Plan 6 / spec §11):** Account 등록/관리 ✅(AccountsClient 생성+멤버 초대 in detail), 미팅↔Account 연결·공유 토글 ✅(AccountSection: link/share), Account 상세(멤버·공유미팅·인사이트 유형필터·문서) ✅. 비-Obsidian 팀원 열람 ✅(웹 상세).
- **검증 방식:** 프런트 테스트 프레임워크 없음 → `npm run lint`+`npm run build`가 게이트(스펙 명시). 수동 UX 확인은 배포 후 사용자 몫.
- **Placeholder:** 컴포넌트 전부 실제 코드. `AppLayout`/`useAuth` import 경로·`MeetingDetailClient` 정확한 삽입 위치는 "파일 열어 확인" 명시(근사).
- **타입 일관성:** `accountApi.{list,get,create,addMember,meetings,insights,listDocuments,getDocument}` + `meetingAccountApi.{link,shareToAccount}` ↔ Plan 1-5 엔드포인트/응답 일치. 타입 `Account/AccountSummary/AccountMember/AccountMeetingRef/AccountInsight/AccountDocument`.
- **TS strict 주의:** `any` 금지(`err instanceof Error` 사용), `import type` 사용, 응답 optional 체이닝(`?.accounts ?? []`). React 19/Next 16 params Promise 패턴(server page).
- **확인 필요(구현자):** `AppLayout`/`useAuth`/`Sidebar` 정확한 export 형태(default vs named) — `kb/page.tsx`·`Sidebar.tsx` 열어 확인. `MeetingDetail` 타입에 `accountId`/`sharedToAccount` 노출 여부 — 없으면 optional 추가. `surface-lowest`/`glass-panel`/`primary-hover` 유틸 존재 확인(globals.css).

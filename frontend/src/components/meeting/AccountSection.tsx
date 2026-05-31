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

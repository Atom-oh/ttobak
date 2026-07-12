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
        <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">Accounts</h2>
        <button
          onClick={() => setShowForm((v) => !v)}
          className="bg-primary hover:bg-primary-hover text-white dark:text-background-dark rounded-lg font-semibold text-sm px-4 py-2 flex items-center gap-1"
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
            className="bg-primary hover:bg-primary-hover text-white dark:text-background-dark rounded-lg font-semibold text-sm px-4 py-2 disabled:opacity-50"
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
        <div className="text-center py-16 text-slate-400 dark:text-text-muted">
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
                <span className="font-medium text-slate-900 dark:text-text-main">{a.name}</span>
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

'use client';

import { useState, useEffect, useCallback } from 'react';
import { settingsApi } from '@/lib/api';
import type { IntegrationsResponse } from '@/types/meeting';

export function IntegrationSettings() {
  const [integrations, setIntegrations] = useState<IntegrationsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [notionKey, setNotionKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const fetchIntegrations = useCallback(async () => {
    try {
      setLoading(true);
      const data = await settingsApi.getIntegrations();
      setIntegrations(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load integrations');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchIntegrations();
  }, [fetchIntegrations]);

  const handleSaveNotion = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!notionKey.trim()) return;

    setSaving(true);
    setError(null);
    setSuccess(null);

    try {
      await settingsApi.saveNotionKey(notionKey.trim());
      setNotionKey('');
      setSuccess('Notion connected successfully');
      await fetchIntegrations();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save Notion key');
    } finally {
      setSaving(false);
    }
  };

  const handleDisconnectNotion = async () => {
    if (!confirm('Are you sure you want to disconnect Notion?')) return;

    setDeleting(true);
    setError(null);
    setSuccess(null);

    try {
      await settingsApi.deleteNotionKey();
      setSuccess('Notion disconnected');
      await fetchIntegrations();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to disconnect Notion');
    } finally {
      setDeleting(false);
    }
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {/* Error/Success Messages */}
      {error && (
        <div className="p-4 bg-red-50 dark:bg-red-900/20 text-red-600 dark:text-red-400 text-sm rounded-lg">
          {error}
        </div>
      )}
      {success && (
        <div className="p-4 bg-green-50 dark:bg-green-900/20 text-green-600 dark:text-green-400 text-sm rounded-lg">
          {success}
        </div>
      )}

      {/* Notion Integration */}
      <div className="bg-white rounded-xl border border-slate-200 p-6 dark:glass-panel">
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 bg-slate-100 dark:bg-white/5 rounded-lg flex items-center justify-center">
              <svg className="w-6 h-6" viewBox="0 0 100 100" fill="none">
                <path
                  d="M6.017 4.313l55.333-4.087c6.797-.583 8.543-.19 12.817 2.917l17.663 12.443c2.913 2.14 3.883 2.723 3.883 5.053v68.243c0 4.277-1.553 6.807-6.99 7.193L24.467 99.967c-4.08.193-6.023-.39-8.16-3.113L3.3 79.94c-2.333-3.113-3.3-5.443-3.3-8.167V11.113c0-3.497 1.553-6.413 6.017-6.8z"
                  fill="#fff"
                />
                <path
                  fillRule="evenodd"
                  clipRule="evenodd"
                  d="M61.35.227l-55.333 4.087C1.553 4.7 0 7.617 0 11.113v60.66c0 2.723.967 5.053 3.3 8.167l13.007 16.913c2.137 2.723 4.08 3.307 8.16 3.113l64.257-3.89c5.433-.387 6.99-2.917 6.99-7.193V20.64c0-2.21-.873-2.847-3.443-4.733L74.167 3.143C69.893.057 68.147-.35 61.35.227zM25.663 24.493c-5.423.387-6.657.467-9.763-1.983l-6.987-5.44c-.78-.78-.39-1.553.973-1.747l52.893-3.887c4.47-.39 6.803.973 8.537 2.333l8.537 6.223c.39.193.97 1.17 0 1.17l-54.583 3.137v.193zm-7.377 68.157V35.823c0-2.53.78-3.697 3.113-3.893l58.77-3.303c2.14-.193 3.11 1.167 3.11 3.693v56.633c0 2.53-1.553 4.083-3.887 4.28l-55.73 3.11c-2.33.193-5.377-.387-5.377-3.693zm54.777-54.45c.39 1.75 0 3.5-1.75 3.7l-2.717.58v41.357c-2.33 1.167-4.46 1.94-6.217 1.94-2.913 0-3.69-.973-5.83-3.5L40.603 49.497v30.273l5.62 1.363s0 3.5-4.857 3.5l-13.397.777c-.39-.78 0-2.723 1.36-3.113l3.5-.973V41.88l-4.857-.39c-.39-1.75.584-4.277 3.307-4.473l14.367-.97 17.277 26.58V36.993l-4.663-.583c-.39-2.14 1.167-3.693 3.11-3.89l13.397-.78z"
                  fill="#000"
                />
              </svg>
            </div>
            <div>
              <h3 className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9]">Notion</h3>
              <p className="text-sm text-slate-500 dark:text-[#849396]">Export meeting notes to Notion</p>
            </div>
          </div>
          <span
            className={`text-xs font-semibold px-2 py-1 rounded-full ${
              integrations?.notion?.configured
                ? 'bg-green-100 text-green-700 dark:bg-[#00E5FF]/10 dark:text-[#00E5FF]'
                : 'bg-slate-100 text-slate-600 dark:bg-white/5 dark:text-[#849396]'
            }`}
          >
            {integrations?.notion?.configured ? 'Connected' : 'Not connected'}
          </span>
        </div>

        {integrations?.notion?.configured ? (
          <div className="space-y-4">
            <div className="flex items-center gap-2 p-3 bg-slate-50 dark:bg-[#0e0e13] dark:border dark:border-white/10 rounded-lg">
              <span className="material-symbols-outlined text-slate-400 dark:text-[#849396]">key</span>
              <span className="text-sm text-slate-600 dark:text-[#bac9cc] font-mono">
                {integrations.notion.maskedKey || '••••••••••••'}
              </span>
            </div>
            <button
              onClick={handleDisconnectNotion}
              disabled={deleting}
              className="flex items-center gap-2 px-4 py-2 text-sm font-semibold text-red-600 hover:bg-red-50 dark:hover:bg-red-900/20 rounded-lg transition-colors disabled:opacity-50"
            >
              {deleting ? (
                <>
                  <span className="animate-spin rounded-full h-4 w-4 border-2 border-red-600 border-t-transparent" />
                  Disconnecting...
                </>
              ) : (
                <>
                  <span className="material-symbols-outlined text-lg">link_off</span>
                  Disconnect
                </>
              )}
            </button>
          </div>
        ) : (
          <form onSubmit={handleSaveNotion} className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-1">
                Notion Integration Token
              </label>
              <div className="flex items-center gap-2">
                <div className="relative flex-1">
                  <input
                    type={showKey ? 'text' : 'password'}
                    value={notionKey}
                    onChange={(e) => setNotionKey(e.target.value)}
                    placeholder="secret_..."
                    className="w-full px-4 py-2.5 text-sm bg-slate-100 dark:bg-[#0e0e13] dark:border dark:border-white/10 dark:text-[#e4e1e9] border-none rounded-lg focus:ring-2 focus:ring-primary/20 dark:placeholder:text-[#849396] placeholder:text-slate-400 font-mono"
                  />
                  <button
                    type="button"
                    onClick={() => setShowKey(!showKey)}
                    className="absolute right-3 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-[#bac9cc]"
                  >
                    <span className="material-symbols-outlined text-lg">
                      {showKey ? 'visibility_off' : 'visibility'}
                    </span>
                  </button>
                </div>
              </div>
              <p className="mt-2 text-xs text-slate-500">
                Create an integration at{' '}
                <a
                  href="https://www.notion.so/my-integrations"
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-primary hover:underline"
                >
                  notion.so/my-integrations
                </a>
              </p>
            </div>
            <button
              type="submit"
              disabled={!notionKey.trim() || saving}
              className="flex items-center gap-2 px-4 py-2 bg-primary text-white dark:text-[#09090E] rounded-lg font-semibold text-sm hover:bg-primary/90 disabled:opacity-50 transition-colors dark:shadow-[0_0_15px_rgba(0,229,255,0.4)]"
            >
              {saving ? (
                <>
                  <span className="animate-spin rounded-full h-4 w-4 border-2 border-white border-t-transparent" />
                  Connecting...
                </>
              ) : (
                <>
                  <span className="material-symbols-outlined text-lg">link</span>
                  Connect
                </>
              )}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}

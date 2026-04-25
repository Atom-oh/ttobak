'use client';

import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { IntegrationSettings } from '@/components/IntegrationSettings';
import { CrawlerSettings } from '@/components/CrawlerSettings';
import { McpGuide } from '@/components/McpGuide';
import { CustomDictionary } from '@/components/CustomDictionary';

export default function SettingsPage() {
  const { user, isLoading, isAuthenticated } = useAuth();

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    // Redirect to home for login
    if (typeof window !== 'undefined') {
      window.location.href = '/';
    }
    return null;
  }

  return (
    <AppLayout activePath="/settings">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-4 justify-between border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">settings</span>
          </div>
          <h1 className="text-slate-900 dark:text-[#e4e1e9] dark:font-[var(--font-headline)] text-xl font-bold leading-tight tracking-tight">
            Settings
          </h1>
        </div>
      </header>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 max-w-4xl w-full space-y-8">
          {/* Page Header */}
          <div>
            <h2 className="hidden lg:block text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-[var(--font-headline)] dark:text-[#e4e1e9]">Settings</h2>
            <h2 className="lg:hidden text-2xl font-extrabold text-slate-900 dark:text-[#e4e1e9] dark:font-[var(--font-headline)] tracking-tight">Settings</h2>
            <p className="text-slate-600 dark:text-[#849396] mt-2">Manage your account and integrations.</p>
          </div>

          {/* Profile Section */}
          <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
            <h3 className="section-header mb-4">Profile</h3>
            <div className="flex items-center gap-4 dark:glass-panel dark:rounded-xl dark:p-4">
              <div className="w-14 h-14 rounded-full bg-slate-100 dark:bg-white/5 flex items-center justify-center text-slate-600 dark:text-[#00E5FF] text-xl font-bold">
                {user?.name?.charAt(0) || user?.email?.charAt(0).toUpperCase() || '?'}
              </div>
              <div>
                <p className="text-base font-semibold text-slate-900 dark:text-[#e4e1e9]">
                  {user?.name || 'User'}
                </p>
                <p className="text-sm text-slate-400 dark:text-[#849396]">{user?.email}</p>
              </div>
            </div>
          </section>

          {/* Integrations Section */}
          <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
            <h3 className="section-header mb-4">
              Integrations
            </h3>
            <IntegrationSettings />
          </section>

          {/* Crawler Sources Section */}
          <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
            <h3 className="section-header mb-4">Crawler Sources</h3>
            <CrawlerSettings />
          </section>

          {/* Custom Dictionary Section */}
          <section className="lg:pb-8 lg:border-b lg:border-slate-200 dark:lg:border-white/10">
            <h3 className="section-header mb-4">Custom Dictionary</h3>
            <CustomDictionary />
          </section>

          {/* Claude Code MCP Section */}
          <section>
            <h3 className="section-header mb-4">
              Developer Tools
            </h3>
            <McpGuide />
          </section>
        </div>
      </div>
    </AppLayout>
  );
}

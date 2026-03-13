'use client';

import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { IntegrationSettings } from '@/components/IntegrationSettings';

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
      {/* Desktop Header */}
      <header className="hidden lg:flex h-16 border-b border-slate-200 dark:border-slate-800 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md items-center justify-between px-8 shrink-0">
        <div className="flex items-center gap-2 text-slate-500 text-sm">
          <span>Workspace</span>
          <span className="material-symbols-outlined text-xs">chevron_right</span>
          <span className="text-slate-900 dark:text-slate-100 font-semibold">Settings</span>
        </div>
      </header>

      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-slate-900 px-4 py-4 justify-between border-b border-slate-100 dark:border-slate-800 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">settings</span>
          </div>
          <h1 className="text-slate-900 dark:text-slate-100 text-xl font-bold leading-tight tracking-tight">
            Settings
          </h1>
        </div>
      </header>

      {/* Content Area */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:p-8 max-w-4xl mx-auto w-full space-y-8">
          {/* Page Header */}
          <div>
            <h2 className="text-2xl lg:text-3xl font-extrabold text-slate-900 dark:text-white tracking-tight">
              Settings
            </h2>
            <p className="text-slate-500 mt-1">Manage your account and integrations.</p>
          </div>

          {/* Profile Section */}
          <section>
            <h3 className="text-lg font-semibold text-slate-900 dark:text-white mb-4">Profile</h3>
            <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-6">
              <div className="flex items-center gap-4">
                <div className="w-16 h-16 rounded-full bg-primary/10 flex items-center justify-center text-primary text-2xl font-bold">
                  {user?.name?.charAt(0) || user?.email?.charAt(0).toUpperCase() || '?'}
                </div>
                <div>
                  <p className="text-lg font-semibold text-slate-900 dark:text-white">
                    {user?.name || 'User'}
                  </p>
                  <p className="text-sm text-slate-500">{user?.email}</p>
                </div>
              </div>
            </div>
          </section>

          {/* Integrations Section */}
          <section>
            <h3 className="text-lg font-semibold text-slate-900 dark:text-white mb-4">
              Integrations
            </h3>
            <IntegrationSettings />
          </section>
        </div>
      </div>
    </AppLayout>
  );
}

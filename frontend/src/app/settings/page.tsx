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
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 max-w-4xl w-full space-y-8">
          {/* Page Header */}
          <div>
            <h2 className="hidden lg:block notion-title">Settings</h2>
            <h2 className="lg:hidden text-2xl font-extrabold text-slate-900 dark:text-white tracking-tight">Settings</h2>
            <p className="text-text-secondary mt-2">Manage your account and integrations.</p>
          </div>

          {/* Profile Section */}
          <section className="lg:pb-8 lg:border-b lg:border-[var(--notion-divider)]">
            <h3 className="notion-subheading mb-4">Profile</h3>
            <div className="flex items-center gap-4">
              <div className="w-14 h-14 rounded-full bg-surface-secondary flex items-center justify-center text-text-secondary text-xl font-bold">
                {user?.name?.charAt(0) || user?.email?.charAt(0).toUpperCase() || '?'}
              </div>
              <div>
                <p className="text-base font-semibold text-text-primary">
                  {user?.name || 'User'}
                </p>
                <p className="text-sm text-text-muted">{user?.email}</p>
              </div>
            </div>
          </section>

          {/* Integrations Section */}
          <section className="lg:pb-8 lg:border-b lg:border-[var(--notion-divider)]">
            <h3 className="notion-subheading mb-4">
              Integrations
            </h3>
            <IntegrationSettings />
          </section>
        </div>
      </div>
    </AppLayout>
  );
}

'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import AccountsClient from '@/components/AccountsClient';

export default function AccountsPage() {
  const { isLoading, isAuthenticated } = useAuth();
  const router = useRouter();
  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) {
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

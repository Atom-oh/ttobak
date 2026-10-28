'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';
import { triggerAuthFailure, useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { getIdToken, refreshSession } from '@/lib/auth';
import { installBillingBridge, type BillingBridgeHost } from '@/lib/billingBridge';

// Hosts the static Billing dashboard (public/billing-app) in a same-origin
// iframe. The iframe reads tokens from window.parent.ttobakBilling.
export default function BillingPage() {
  const { isLoading, isAuthenticated } = useAuth();
  const router = useRouter();

  useEffect(() => {
    if (!isLoading && !isAuthenticated) router.replace('/');
  }, [isLoading, isAuthenticated, router]);

  useEffect(() => {
    if (!isAuthenticated) return;
    return installBillingBridge(window as unknown as BillingBridgeHost, {
      getToken: getIdToken,
      refresh: refreshSession,
      onSignedOut: triggerAuthFailure,
    });
  }, [isAuthenticated]);

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

  // Only reached after the auth gate (client side), so window is available.
  const upload = new URLSearchParams(window.location.search).get('view') === 'upload';

  return (
    <AppLayout activePath="/billing">
      <iframe
        title="Billing"
        src={upload ? '/billing-app/upload.html' : '/billing-app/index.html'}
        className="flex-1 w-full min-h-0 border-0 bg-transparent"
      />
    </AppLayout>
  );
}

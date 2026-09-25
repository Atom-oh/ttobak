'use client';

import { useEffect, useRef } from 'react';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { dispatchNativeControl } from '@/lib/nativeControl';
import { isTauri, markNativeControlReady, onNativeControlRequest, replyNativeControl } from '@/lib/tauri';

/** Mac app only: receives MCP control requests forwarded by the app's local
 * socket and routes them to the record page (ADR-046). Renders nothing and
 * does nothing in a browser or an older app build. */
export function NativeControlBridge() {
  const { isAuthenticated, isLoading } = useAuth();
  const router = useRouter();
  const authenticatedRef = useRef(isAuthenticated);

  useEffect(() => {
    authenticatedRef.current = isAuthenticated;
    if (!isTauri() || isLoading) return;
    void markNativeControlReady(isAuthenticated);
  }, [isAuthenticated, isLoading]);

  useEffect(() => {
    if (!isTauri()) return;
    return onNativeControlRequest((request) => {
      if (!authenticatedRef.current) {
        void replyNativeControl(request.requestId, {
          ok: false,
          error: { code: 'login_required', message: 'Sign in to TTOBAK in the Mac app first.' },
        });
        return;
      }
      if (dispatchNativeControl(request)) return;
      if (request.action === 'stop') {
        void replyNativeControl(request.requestId, {
          ok: false,
          error: { code: 'not_recording', message: 'The TTOBAK app is not recording.' },
        });
        return;
      }
      router.push('/record');
    });
  }, [router]);

  return null;
}

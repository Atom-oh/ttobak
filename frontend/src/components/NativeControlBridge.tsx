'use client';

import { useEffect, useRef, useState } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { dismissDialogs, isDialogOpen } from '@/lib/dialog';
import { clearQueuedNativeControl, dispatchNativeControl } from '@/lib/nativeControl';
import { isTauri, markNativeControlReady, onNativeControlRequest, replyNativeControl } from '@/lib/tauri';

/** Mac app only: receives MCP control requests forwarded by the app's local
 * socket and routes them to the record page (ADR-046). Renders nothing and
 * does nothing in a browser or an older app build. */
export function NativeControlBridge() {
  const { isAuthenticated, isLoading } = useAuth();
  const router = useRouter();
  const pathname = usePathname();
  // In-app dialogs belong to the page that opened them (see lib/dialog).
  useEffect(() => { dismissDialogs(); }, [pathname]);
  const authenticatedRef = useRef(isAuthenticated);
  // Readiness is published only after the request listener is registered,
  // so the app never emits a request nobody hears.
  const [listening, setListening] = useState(false);

  useEffect(() => {
    authenticatedRef.current = isAuthenticated;
    if (!isAuthenticated) clearQueuedNativeControl();
    if (!isTauri() || isLoading || !listening) return;
    void markNativeControlReady(isAuthenticated);
  }, [isAuthenticated, isLoading, listening]);

  // Not ready while the page is going away (reload, navigation out of the
  // app), so the app answers app_not_ready instead of waiting on a page that
  // no longer listens.
  useEffect(() => {
    if (!isTauri()) return;
    const leave = () => void markNativeControlReady(false, false);
    window.addEventListener('pagehide', leave);
    return () => {
      window.removeEventListener('pagehide', leave);
      leave();
    };
  }, []);

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
      // A start would navigate or act under a confirmation the user has not
      // answered; refuse it instead of running a stale continuation later.
      if (request.action === 'start' && isDialogOpen()) {
        void replyNativeControl(request.requestId, {
          ok: false,
          error: { code: 'busy', message: 'A confirmation dialog is open in the TTOBAK app; answer it and retry.' },
        });
        return;
      }
      const { handled, superseded, startPending } = dispatchNativeControl(request);
      if (superseded) {
        void replyNativeControl(superseded.requestId, {
          ok: false,
          error: { code: 'busy', message: 'A newer start request replaced this one.' },
        });
      }
      if (handled) return;
      if (request.action === 'stop' && startPending) {
        void replyNativeControl(request.requestId, {
          ok: false,
          error: { code: 'busy', message: 'The recording is still starting; try again in a moment.' },
        });
        return;
      }
      if (request.action === 'stop') {
        void replyNativeControl(request.requestId, {
          ok: false,
          error: { code: 'not_recording', message: 'The TTOBAK app is not recording.' },
        });
        return;
      }
      // A dialog opened on the current screen must not survive this move.
      dismissDialogs();
      router.push('/record');
    }, () => setListening(true));
  }, [router]);

  return null;
}

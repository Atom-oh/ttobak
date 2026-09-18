'use client';

import React, { createContext, useContext, useEffect, useState, useCallback, useRef } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { AuthUser, getCurrentUser, signIn, signOut, completeNewPassword, isNewPasswordRequired, NewPasswordRequiredResult, SignInResult } from '@/lib/auth';
import { EmailVerificationPanel } from './EmailVerificationPanel';
import { proactiveSearchStore, resetProactiveClaims, setProactiveSearchUser } from '@/lib/proactiveSearch';

interface AuthContextType {
  user: AuthUser | null;
  isLoading: boolean;
  isAuthenticated: boolean;
  isAdmin: boolean;
  login: (email: string, password: string) => Promise<SignInResult>;
  completeNewPassword: (challenge: NewPasswordRequiredResult, newPassword: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthContextType | null>(null);

// Module-level callback so api.ts can trigger logout without circular React deps
let authFailureCallback: (() => void) | null = null;
export function setAuthFailureCallback(cb: (() => void) | null) {
  authFailureCallback = cb;
}
export function triggerAuthFailure() {
  authFailureCallback?.();
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [bootstrappedUserId, setBootstrappedUserId] = useState<string | null>(null);
  const [bootstrapError, setBootstrapError] = useState('');
  const [pendingGrants, setPendingGrants] = useState(0);
  const [retryPending, setRetryPending] = useState(false);
  const [bootstrapAttempt, setBootstrapAttempt] = useState(0);
  const activeUserId = useRef<string | null>(null);
  activeUserId.current = user?.userId ?? null;
  const router = useRouter();
  const pathname = usePathname();
  const wasAuthenticated = useRef(false);

  useEffect(() => {
    setAuthFailureCallback(() => {
      setUser(null);
      localStorage.removeItem('idToken');
      localStorage.removeItem('accessToken');
      localStorage.removeItem('refreshToken');
      // This session-expiry teardown does NOT go through auth.ts's signOut(),
      // so it must drop the proactive-search opt-in itself — the flag is
      // origin-wide localStorage, and one user's external-transmission
      // consent must not carry over to whoever logs in next on a shared
      // browser. Claims are cleared too so the ended session's fired
      // questions can't shadow the next user's.
      proactiveSearchStore.clear();
      resetProactiveClaims();
    });
    return () => setAuthFailureCallback(null);
  }, []);

  // Bind the proactive-search opt-in store to the signed-in user. This is
  // what actually closes the shared-browser consent gap: teardown callbacks
  // only cover explicit signOut and 401-triggered expiry, but a session that
  // lapses QUIETLY (browser closed, tokens expire, next visitor logs in)
  // fires neither — with the store keyed per user, the next user reads their
  // OWN key (default OFF) no matter how the previous session ended, and the
  // same user returning keeps their choice.
  useEffect(() => {
    setProactiveSearchUser(user?.userId ?? null);
  }, [user]);

  // Redirect to login when auth expires on non-root pages
  useEffect(() => {
    if (!isLoading && wasAuthenticated.current && !user && pathname !== '/') {
      router.push('/');
    }
    if (user) wasAuthenticated.current = true;
  }, [user, isLoading, pathname, router]);

  useEffect(() => {
    let cancelled = false;
    // Both bootstrap sources resolve asynchronously so SSR and hydration start
    // in the same loading state, including when development auth is enabled.
    const currentUser: Promise<AuthUser | null> = process.env.NEXT_PUBLIC_DEV_AUTH === 'true'
      ? Promise.resolve({ userId: 'dev-user', email: 'dev@ttobak.io', name: 'Dev User', groups: ['admins'], isAdmin: true })
      : getCurrentUser();
    currentUser
      .then((nextUser) => { if (!cancelled) setUser(nextUser); })
      .catch(() => { if (!cancelled) setUser(null); })
      .finally(() => { if (!cancelled) setIsLoading(false); });
    return () => { cancelled = true; };
  }, []);

  const bootstrapUserId = user?.userId;
  const bootstrapEmailVerified = user?.emailVerified;
  useEffect(() => {
    let cancelled = false;
    if (!bootstrapUserId) { setBootstrappedUserId(null); setBootstrapError(''); setPendingGrants(0); setRetryPending(false); return; }
    if (process.env.NEXT_PUBLIC_DEV_AUTH === 'true') { setBootstrappedUserId(bootstrapUserId); return; }
    const expectedUserId = bootstrapUserId;
    setBootstrapError('');
    import('@/lib/api').then(({ sessionApi }) => sessionApi.bootstrap(expectedUserId)).then(result => {
      if (cancelled || activeUserId.current !== expectedUserId) return;
      setPendingGrants(result.pendingGrants);
      setRetryPending(Boolean(result.retryPending));
      setBootstrappedUserId(expectedUserId);
    }).catch(error => {
      if (!cancelled && activeUserId.current === expectedUserId) setBootstrapError(error instanceof Error ? error.message : '계정 연결을 완료하지 못했습니다.');
    });
    return () => { cancelled = true; };
  }, [bootstrapUserId, bootstrapEmailVerified, bootstrapAttempt]);

  const login = useCallback(async (email: string, password: string) => {
    const result = await signIn(email, password);
    if (!isNewPasswordRequired(result)) {
      setUser(result);
    }
    return result;
  }, []);

  const completeNewPasswordAction = useCallback(
    async (challenge: NewPasswordRequiredResult, newPassword: string) => {
      const authUser = await completeNewPassword(challenge, newPassword);
      setUser(authUser);
    },
    []
  );

  const logout = useCallback(async () => {
    await signOut();
    setUser(null);
  }, []);

  return (
    <AuthContext.Provider
      value={{
        user,
        isLoading: isLoading || (!!user && bootstrappedUserId !== user.userId),
        isAuthenticated: !!user && bootstrappedUserId === user.userId,
        isAdmin: !!user?.isAdmin,
        login,
        completeNewPassword: completeNewPasswordAction,
        logout,
      }}
    >
      {user && bootstrapError && (
        <div role="alert" className="relative z-50 bg-red-50 p-4 text-red-900">
          <p>{bootstrapError}</p>
          <button type="button" onClick={() => setBootstrapAttempt(value => value + 1)} className="underline">계정 연결 다시 시도</button>
          <button type="button" onClick={logout} className="ml-4 underline">로그아웃</button>
        </div>
      )}
      {user && !user.emailVerified && process.env.NEXT_PUBLIC_DEV_AUTH !== 'true' && (
        <EmailVerificationPanel key={user.userId} userId={user.userId} email={user.email} onVerified={verified => {
          if (activeUserId.current !== verified.userId) return;
          setUser(verified); setBootstrapAttempt(value => value + 1);
        }} />
      )}
      {user && user.emailVerified && (pendingGrants > 0 || retryPending) && (
        <div role="status" className="relative z-50 bg-amber-50 p-3 text-sm text-amber-900">
          일부 초대가 아직 처리 중입니다.
          <button type="button" className="ml-2 underline" onClick={() => setBootstrapAttempt(value => value + 1)}>초대 연결 다시 시도</button>
        </div>
      )}
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
}

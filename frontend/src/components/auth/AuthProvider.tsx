'use client';

import React, { createContext, useContext, useEffect, useState, useCallback, useRef } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { AuthUser, getCurrentUser, signIn, signOut, signUp, confirmSignUp, completeNewPassword, isNewPasswordRequired, NewPasswordRequiredResult, SignInResult } from '@/lib/auth';

interface AuthContextType {
  user: AuthUser | null;
  isLoading: boolean;
  isAuthenticated: boolean;
  isAdmin: boolean;
  login: (email: string, password: string) => Promise<SignInResult>;
  completeNewPassword: (challenge: NewPasswordRequiredResult, newPassword: string) => Promise<void>;
  logout: () => Promise<void>;
  register: (email: string, password: string, name?: string) => Promise<void>;
  confirmRegistration: (email: string, code: string) => Promise<void>;
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
  const router = useRouter();
  const pathname = usePathname();
  const wasAuthenticated = useRef(false);

  useEffect(() => {
    setAuthFailureCallback(() => {
      setUser(null);
      localStorage.removeItem('idToken');
      localStorage.removeItem('accessToken');
      localStorage.removeItem('refreshToken');
    });
    return () => setAuthFailureCallback(null);
  }, []);

  // Redirect to login when auth expires on non-root pages
  useEffect(() => {
    if (!isLoading && wasAuthenticated.current && !user && pathname !== '/') {
      router.push('/');
    }
    if (user) wasAuthenticated.current = true;
  }, [user, isLoading, pathname, router]);

  useEffect(() => {
    // Dev mode: skip Cognito auth when env var is set
    if (process.env.NEXT_PUBLIC_DEV_AUTH === 'true') {
      setUser({ userId: 'dev-user', email: 'dev@ttobak.io', name: 'Dev User', groups: ['admins'], isAdmin: true });
      setIsLoading(false);
      return;
    }
    getCurrentUser()
      .then(setUser)
      .catch(() => setUser(null))
      .finally(() => setIsLoading(false));
  }, []);

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

  const register = useCallback(async (email: string, password: string, name?: string) => {
    await signUp(email, password, name);
  }, []);

  const confirmRegistration = useCallback(async (email: string, code: string) => {
    await confirmSignUp(email, code);
  }, []);

  return (
    <AuthContext.Provider
      value={{
        user,
        isLoading,
        isAuthenticated: !!user,
        isAdmin: !!user?.isAdmin,
        login,
        completeNewPassword: completeNewPasswordAction,
        logout,
        register,
        confirmRegistration,
      }}
    >
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

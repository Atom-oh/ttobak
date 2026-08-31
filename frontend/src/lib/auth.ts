'use client';

import {
  CognitoUserPool,
  CognitoUser,
  AuthenticationDetails,
  CognitoUserSession,
  CognitoRefreshToken,
} from 'amazon-cognito-identity-js';

import { proactiveSearchStore, resetProactiveClaims } from './proactiveSearch';
import { getRuntimeConfig } from './runtimeConfig';

let userPoolPromise: Promise<CognitoUserPool> | null = null;

function getUserPool(): Promise<CognitoUserPool> {
  if (!userPoolPromise) {
    userPoolPromise = (async () => {
      const cfg = await getRuntimeConfig();
      return new CognitoUserPool({
        UserPoolId: cfg.cognito.userPoolId,
        ClientId: cfg.cognito.userPoolClientId,
      });
    })().catch((e) => {
      // Drop the cached rejection so the next call can retry once /config.json is fixed
      userPoolPromise = null;
      throw e;
    });
  }
  return userPoolPromise;
}

export interface AuthUser {
  userId: string;
  email: string;
  name?: string;
  groups: string[];
  isAdmin: boolean;
}

// buildAuthUser maps a decoded Cognito ID token payload to an AuthUser.
// The "cognito:groups" claim drives admin gating (matches the backend's
// middleware.IsAdmin check for the "admins" group).
function buildAuthUser(payload: Record<string, unknown>): AuthUser {
  const groups = Array.isArray(payload['cognito:groups'])
    ? (payload['cognito:groups'] as string[])
    : [];
  return {
    userId: payload.sub as string,
    email: payload.email as string,
    name: payload.name as string | undefined,
    groups,
    isAdmin: groups.includes('admins'),
  };
}

export interface NewPasswordRequiredResult {
  challenge: 'NEW_PASSWORD_REQUIRED';
  cognitoUser: CognitoUser;
  /** Attributes Cognito returned alongside the challenge (e.g. email, name). */
  userAttributes: Record<string, string>;
  /** Attribute names Cognito actually requires to complete the challenge — usually empty. */
  requiredAttributes: string[];
}

export type SignInResult = AuthUser | NewPasswordRequiredResult;

export function isNewPasswordRequired(
  result: SignInResult
): result is NewPasswordRequiredResult {
  return (result as NewPasswordRequiredResult).challenge === 'NEW_PASSWORD_REQUIRED';
}

export async function signIn(
  email: string,
  password: string
): Promise<SignInResult> {
  const pool = await getUserPool();
  return new Promise((resolve, reject) => {
    const authDetails = new AuthenticationDetails({
      Username: email,
      Password: password,
    });

    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: pool,
    });

    cognitoUser.authenticateUser(authDetails, {
      onSuccess: (session: CognitoUserSession) => {
        const idToken = session.getIdToken();
        const payload = idToken.decodePayload();

        localStorage.setItem('idToken', idToken.getJwtToken());
        localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
        localStorage.setItem('refreshToken', session.getRefreshToken().getToken());

        resolve(buildAuthUser(payload));
      },
      onFailure: (err) => {
        reject(err);
      },
      newPasswordRequired: (userAttributes, requiredAttributes) => {
        resolve({
          challenge: 'NEW_PASSWORD_REQUIRED',
          cognitoUser,
          userAttributes,
          requiredAttributes: requiredAttributes ?? [],
        });
      },
    });
  });
}

/**
 * Completes the NEW_PASSWORD_REQUIRED challenge returned by signIn() for
 * users created with a temporary password (e.g. admin-created accounts).
 */
export async function completeNewPassword(
  result: NewPasswordRequiredResult,
  newPassword: string
): Promise<AuthUser> {
  return new Promise((resolve, reject) => {
    // Cognito rejects the challenge if any attribute it didn't ask for is
    // resubmitted (e.g. echoing back an already-set "email" throws
    // "Cannot modify an already provided email") — only send attributes it
    // actually listed as required.
    const attributesToSubmit: Record<string, string> = {};
    for (const name of result.requiredAttributes) {
      const value = result.userAttributes[name];
      if (value !== undefined) attributesToSubmit[name] = value;
    }

    result.cognitoUser.completeNewPasswordChallenge(newPassword, attributesToSubmit, {
      onSuccess: (session: CognitoUserSession) => {
        const idToken = session.getIdToken();
        const payload = idToken.decodePayload();

        localStorage.setItem('idToken', idToken.getJwtToken());
        localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
        localStorage.setItem('refreshToken', session.getRefreshToken().getToken());

        resolve(buildAuthUser(payload));
      },
      onFailure: (err) => {
        reject(err);
      },
    });
  });
}

export async function signOut(): Promise<void> {
  const pool = await getUserPool();
  const cognitoUser = pool.getCurrentUser();

  if (cognitoUser) {
    cognitoUser.signOut();
  }

  localStorage.removeItem('idToken');
  localStorage.removeItem('accessToken');
  localStorage.removeItem('refreshToken');
  // The proactive-search opt-in consents to sending conversation-derived
  // queries to an external search provider — drop this user's stored
  // consent and the in-memory claim state on explicit sign-out (symmetric
  // with AuthProvider's expiry teardown; the per-user storage key is what
  // covers the quiet-expiry path neither callback sees).
  proactiveSearchStore.clear();
  resetProactiveClaims();
}

export async function getCurrentUser(): Promise<AuthUser | null> {
  const pool = await getUserPool();
  return new Promise((resolve) => {
    const cognitoUser = pool.getCurrentUser();

    if (!cognitoUser) {
      resolve(null);
      return;
    }

    cognitoUser.getSession((err: Error | null, session: CognitoUserSession | null) => {
      if (err || !session || !session.isValid()) {
        resolve(null);
        return;
      }

      const idToken = session.getIdToken();
      const payload = idToken.decodePayload();

      localStorage.setItem('idToken', idToken.getJwtToken());
      localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
      // Sync refresh token — getSession() may have refreshed it internally
      const rt = session.getRefreshToken()?.getToken();
      if (rt) localStorage.setItem('refreshToken', rt);

      resolve(buildAuthUser(payload));
    });
  });
}

export function getIdToken(): string | null {
  if (typeof window === 'undefined') return null;
  return localStorage.getItem('idToken');
}

export async function refreshSession(): Promise<string | null> {
  const pool = await getUserPool();
  return new Promise((resolve) => {
    const cognitoUser = pool.getCurrentUser();

    if (!cognitoUser) {
      resolve(null);
      return;
    }

    const refreshTokenStr = localStorage.getItem('refreshToken');
    if (!refreshTokenStr) {
      // No app-managed refresh token — try SDK's getSession as fallback
      return fallbackGetSession(cognitoUser, resolve);
    }

    const refreshToken = new CognitoRefreshToken({ RefreshToken: refreshTokenStr });
    cognitoUser.refreshSession(refreshToken, (err: Error | null, session: CognitoUserSession | null) => {
      if (err || !session) {
        // Manual refresh failed — try SDK's getSession (may have a valid token in its own storage)
        console.warn('Token refresh failed, trying SDK fallback:', err?.message);
        return fallbackGetSession(cognitoUser, resolve);
      }

      const idToken = session.getIdToken().getJwtToken();
      localStorage.setItem('idToken', idToken);
      localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
      const rt = session.getRefreshToken()?.getToken();
      if (rt) localStorage.setItem('refreshToken', rt);
      resolve(idToken);
    });
  });
}

function fallbackGetSession(
  cognitoUser: CognitoUser,
  resolve: (value: string | null) => void,
): void {
  cognitoUser.getSession((err: Error | null, session: CognitoUserSession | null) => {
    if (err || !session || !session.isValid()) {
      resolve(null);
      return;
    }
    const idToken = session.getIdToken().getJwtToken();
    localStorage.setItem('idToken', idToken);
    localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
    const rt = session.getRefreshToken()?.getToken();
    if (rt) localStorage.setItem('refreshToken', rt);
    resolve(idToken);
  });
}

export async function forgotPassword(email: string): Promise<void> {
  const pool = await getUserPool();
  return new Promise((resolve, reject) => {
    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: pool,
    });

    cognitoUser.forgotPassword({
      onSuccess: () => resolve(),
      onFailure: (err) => reject(err),
    });
  });
}

export async function confirmForgotPassword(
  email: string,
  code: string,
  newPassword: string
): Promise<void> {
  const pool = await getUserPool();
  return new Promise((resolve, reject) => {
    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: pool,
    });

    cognitoUser.confirmPassword(code, newPassword, {
      onSuccess: () => resolve(),
      onFailure: (err) => reject(err),
    });
  });
}

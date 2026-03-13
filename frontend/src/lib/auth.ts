'use client';

import {
  CognitoUserPool,
  CognitoUser,
  AuthenticationDetails,
  CognitoUserAttribute,
  CognitoUserSession,
  CognitoRefreshToken,
} from 'amazon-cognito-identity-js';

const poolData = {
  UserPoolId: process.env.NEXT_PUBLIC_COGNITO_USER_POOL_ID || '',
  ClientId: process.env.NEXT_PUBLIC_COGNITO_CLIENT_ID || '',
};

let userPool: CognitoUserPool | null = null;

function getUserPool(): CognitoUserPool {
  if (!userPool) {
    userPool = new CognitoUserPool(poolData);
  }
  return userPool;
}

export interface AuthUser {
  userId: string;
  email: string;
  name?: string;
}

export async function signUp(
  email: string,
  password: string,
  name?: string
): Promise<void> {
  return new Promise((resolve, reject) => {
    const attributeList: CognitoUserAttribute[] = [];

    attributeList.push(
      new CognitoUserAttribute({ Name: 'email', Value: email })
    );

    if (name) {
      attributeList.push(
        new CognitoUserAttribute({ Name: 'name', Value: name })
      );
    }

    getUserPool().signUp(email, password, attributeList, [], (err) => {
      if (err) {
        reject(err);
        return;
      }
      resolve();
    });
  });
}

export async function confirmSignUp(
  email: string,
  code: string
): Promise<void> {
  return new Promise((resolve, reject) => {
    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: getUserPool(),
    });

    cognitoUser.confirmRegistration(code, true, (err) => {
      if (err) {
        reject(err);
        return;
      }
      resolve();
    });
  });
}

export async function signIn(
  email: string,
  password: string
): Promise<AuthUser> {
  return new Promise((resolve, reject) => {
    const authDetails = new AuthenticationDetails({
      Username: email,
      Password: password,
    });

    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: getUserPool(),
    });

    cognitoUser.authenticateUser(authDetails, {
      onSuccess: (session: CognitoUserSession) => {
        const idToken = session.getIdToken();
        const payload = idToken.decodePayload();

        localStorage.setItem('idToken', idToken.getJwtToken());
        localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
        localStorage.setItem('refreshToken', session.getRefreshToken().getToken());

        resolve({
          userId: payload.sub,
          email: payload.email,
          name: payload.name,
        });
      },
      onFailure: (err) => {
        reject(err);
      },
      newPasswordRequired: () => {
        reject(new Error('New password required'));
      },
    });
  });
}

export async function signOut(): Promise<void> {
  const cognitoUser = getUserPool().getCurrentUser();

  if (cognitoUser) {
    cognitoUser.signOut();
  }

  localStorage.removeItem('idToken');
  localStorage.removeItem('accessToken');
  localStorage.removeItem('refreshToken');
}

export async function getCurrentUser(): Promise<AuthUser | null> {
  return new Promise((resolve) => {
    const cognitoUser = getUserPool().getCurrentUser();

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

      resolve({
        userId: payload.sub,
        email: payload.email,
        name: payload.name,
      });
    });
  });
}

export function getIdToken(): string | null {
  if (typeof window === 'undefined') return null;
  return localStorage.getItem('idToken');
}

export async function refreshSession(): Promise<string | null> {
  return new Promise((resolve) => {
    const cognitoUser = getUserPool().getCurrentUser();

    if (!cognitoUser) {
      resolve(null);
      return;
    }

    const refreshTokenStr = localStorage.getItem('refreshToken');
    if (!refreshTokenStr) {
      resolve(null);
      return;
    }

    const refreshToken = new CognitoRefreshToken({ RefreshToken: refreshTokenStr });
    cognitoUser.refreshSession(refreshToken, (err: Error | null, session: CognitoUserSession | null) => {
      if (err || !session) {
        resolve(null);
        return;
      }

      const idToken = session.getIdToken().getJwtToken();
      localStorage.setItem('idToken', idToken);
      localStorage.setItem('accessToken', session.getAccessToken().getJwtToken());
      resolve(idToken);
    });
  });
}

export async function forgotPassword(email: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: getUserPool(),
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
  return new Promise((resolve, reject) => {
    const cognitoUser = new CognitoUser({
      Username: email,
      Pool: getUserPool(),
    });

    cognitoUser.confirmPassword(code, newPassword, {
      onSuccess: () => resolve(),
      onFailure: (err) => reject(err),
    });
  });
}

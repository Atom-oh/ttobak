// Keep aligned with AuthStack's Cognito password policy. Never alter a password.
export const PASSWORD_REQUIREMENTS = '8자 이상, 영문 소문자와 숫자를 포함해야 합니다.';
export function passwordPolicyError(password: string): string | null {
  return password.length >= 8 && /[a-z]/.test(password) && /[0-9]/.test(password)
    ? null : PASSWORD_REQUIREMENTS;
}

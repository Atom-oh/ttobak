// Display-name rules mirrored from backend service.NormalizeUserName so the
// form can explain a rejection before the request. The server stays
// authoritative; this only improves feedback.
export const MAX_USER_NAME_LENGTH = 64;

// Cc controls plus bidirectional formatting controls (Unicode Bidi_Control).
const FORBIDDEN = /[\p{Cc}؜‎‏‪-‮⁦-⁩]/u;

export type UserNameCheck = { ok: true; name: string } | { ok: false; message: string };

export function checkUserName(raw: string): UserNameCheck {
  const name = raw.trim();
  if (!name) return { ok: false, message: '이름을 입력하세요.' };
  if (Array.from(name).length > MAX_USER_NAME_LENGTH) {
    return { ok: false, message: `이름은 ${MAX_USER_NAME_LENGTH}자 이하로 입력하세요.` };
  }
  if (FORBIDDEN.test(name)) return { ok: false, message: '줄바꿈이나 제어 문자는 사용할 수 없습니다.' };
  return { ok: true, name };
}

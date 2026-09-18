'use client';

import { useState } from 'react';
import { authErrorMessage } from '@/lib/auth-errors';
import { requestEmailVerification, confirmEmailVerification, type AuthUser } from '@/lib/auth';

export function EmailVerificationPanel({ userId, email, onVerified }: { userId: string; email: string; onVerified: (user: AuthUser) => void }) {
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');
  const send = async () => {
    setBusy(true); setError('');
    try { await requestEmailVerification(userId); setNotice('인증 코드 발송을 요청했습니다. 받은 코드를 입력해주세요.'); }
    catch (error) { setError(authErrorMessage(error, '인증 코드 요청에 실패했습니다.')); }
    finally { setBusy(false); }
  };
  const verify = async () => {
    setBusy(true); setError('');
    try { onVerified(await confirmEmailVerification(userId, code)); }
    catch (error) { setError(authErrorMessage(error, '인증에 실패했습니다.')); }
    finally { setBusy(false); }
  };
  return (
    <aside className="relative z-50 border-b border-amber-300 bg-amber-50 p-4 text-sm text-amber-950" aria-label="이메일 인증">
      <p>{email} 이메일 인증이 필요합니다. 비밀번호 찾기와 대기 중인 초대 적용을 위해 인증해주세요.</p>
      {notice && <p role="status">{notice}</p>}
      {error && <p role="alert" className="text-red-700">{error}</p>}
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <button type="button" onClick={send} disabled={busy} className="rounded border px-3 py-1 disabled:opacity-50">인증 코드 받기</button>
        <input aria-label="이메일 인증 코드" autoComplete="one-time-code" maxLength={16} value={code} onChange={event => setCode(event.target.value)} className="rounded border px-3 py-1" />
        <button type="button" onClick={verify} disabled={busy || !code.trim()} className="rounded bg-primary px-3 py-1 text-white disabled:opacity-50">이메일 인증</button>
      </div>
    </aside>
  );
}

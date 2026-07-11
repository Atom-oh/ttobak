'use client';

import { useState } from 'react';
import { settingsApi } from '@/lib/api';

export function InviteUser() {
  const [email, setEmail] = useState('');
  const [name, setName] = useState('');
  const [makeAdmin, setMakeAdmin] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = email.trim().toLowerCase();
    if (!trimmed) return;

    setSubmitting(true);
    setError(null);
    setSuccess(null);

    try {
      const res = await settingsApi.inviteUser({
        email: trimmed,
        name: name.trim() || undefined,
        admin: makeAdmin,
      });
      setSuccess(
        `${res.email} 님에게 초대 이메일을 보냈습니다. 임시 비밀번호와 로그인 링크가 포함되어 있습니다.${
          res.addedToAdmins ? ' (관리자 권한 부여됨)' : ''
        }`
      );
      setEmail('');
      setName('');
      setMakeAdmin(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : '사용자 초대에 실패했습니다');
    } finally {
      setSubmitting(false);
    }
  };

  const inputClass =
    'w-full px-3 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-[#849396]/40 dark:focus:ring-0 dark:focus:border-[#00E5FF]';

  return (
    <div className="dark:glass-panel dark:rounded-xl dark:p-5 space-y-4">
      <p className="text-sm text-slate-600 dark:text-[#849396]">
        이메일 주소를 입력하면 해당 사용자에게 로그인 링크와 임시 비밀번호가 담긴 초대 메일이 발송됩니다.
        초대받은 사용자는 최초 로그인 시 새 비밀번호를 설정하게 됩니다.
      </p>

      {error && (
        <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
          <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
        </div>
      )}
      {success && (
        <div className="bg-emerald-50 dark:bg-emerald-900/20 border border-emerald-200 dark:border-emerald-800 rounded-lg p-3">
          <p className="text-emerald-700 dark:text-emerald-400 text-sm">{success}</p>
        </div>
      )}

      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <label htmlFor="invite-email" className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-1">
            이메일 <span className="text-red-500">*</span>
          </label>
          <input
            id="invite-email"
            type="email"
            required
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="user@example.com"
            className={inputClass}
          />
        </div>

        <div>
          <label htmlFor="invite-name" className="block text-sm font-medium text-slate-700 dark:text-[#bac9cc] mb-1">
            이름 <span className="text-slate-400 dark:text-[#849396] font-normal">(선택)</span>
          </label>
          <input
            id="invite-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="홍길동"
            className={inputClass}
          />
        </div>

        <label className="flex items-center gap-2 cursor-pointer select-none">
          <input
            type="checkbox"
            checked={makeAdmin}
            onChange={(e) => setMakeAdmin(e.target.checked)}
            className="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-white/20 dark:bg-black/30"
          />
          <span className="text-sm text-slate-700 dark:text-[#bac9cc]">관리자 권한 부여</span>
        </label>

        <button
          type="submit"
          disabled={submitting || !email.trim()}
          className="inline-flex items-center gap-2 bg-primary hover:bg-primary/90 text-white font-semibold px-5 py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed dark:bg-[#00E5FF] dark:text-[#001f24] dark:hover:shadow-[0_0_15px_rgba(0,229,255,0.4)]"
        >
          <span className="material-symbols-outlined text-lg">
            {submitting ? 'hourglass_top' : 'person_add'}
          </span>
          {submitting ? '초대 중...' : '사용자 초대'}
        </button>
      </form>
    </div>
  );
}

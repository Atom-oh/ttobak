'use client';

/**
 * "비밀번호 찾기" modal — self-service password recovery, and also the only
 * place a user can complete an admin-triggered force-reset (Settings ›
 * 사용자 관리 › 비밀번호 재설정 calls AdminResetUserPassword, which emails a
 * code but has no UI of its own to consume it). Uses the existing
 * forgotPassword/confirmForgotPassword helpers in lib/auth.ts, which were
 * implemented but never wired to any component until this form.
 */

import { useState } from 'react';
import { forgotPassword, confirmForgotPassword } from '@/lib/auth';

interface ForgotPasswordFormProps {
  onClose: () => void;
}

const inputClass =
  'w-full px-3 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-text-muted/40 dark:focus:ring-primary/30';

export function ForgotPasswordForm({ onClose }: ForgotPasswordFormProps) {
  const [step, setStep] = useState<'request' | 'confirm'>('request');
  const [email, setEmail] = useState('');
  const [code, setCode] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmNewPassword, setConfirmNewPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  const handleRequestCode = async (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = email.trim().toLowerCase();
    if (!trimmed) return;

    setSubmitting(true);
    setError(null);
    try {
      await forgotPassword(trimmed);
      setStep('confirm');
    } catch (err) {
      setError(err instanceof Error ? err.message : '인증 코드 발송에 실패했습니다');
    } finally {
      setSubmitting(false);
    }
  };

  const handleConfirm = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (newPassword !== confirmNewPassword) {
      setError('비밀번호가 일치하지 않습니다');
      return;
    }
    if (newPassword.length < 8) {
      setError('비밀번호는 8자 이상이어야 합니다');
      return;
    }

    setSubmitting(true);
    try {
      await confirmForgotPassword(email.trim().toLowerCase(), code.trim(), newPassword);
      setSuccess('비밀번호가 변경되었습니다. 새 비밀번호로 로그인해주세요.');
    } catch (err) {
      setError(err instanceof Error ? err.message : '비밀번호 재설정에 실패했습니다');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/40 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="bg-white dark:bg-surface-lowest rounded-2xl shadow-2xl w-full max-w-md flex flex-col border border-slate-200 dark:border-white/10"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-slate-100 dark:border-white/5">
          <div>
            <h3 className="font-bold text-slate-900 dark:text-white">비밀번호 찾기</h3>
            <p className="text-xs text-slate-500 dark:text-text-muted mt-0.5">
              {step === 'request'
                ? '가입한 이메일로 인증 코드를 보내드립니다'
                : '이메일로 받은 코드와 새 비밀번호를 입력해주세요'}
            </p>
          </div>
          <button
            onClick={onClose}
            className="p-1.5 rounded-lg text-slate-400 hover:bg-slate-100 dark:hover:bg-white/5 transition-colors"
            aria-label="닫기"
          >
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        <div className="p-5 space-y-4">
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

          {success ? (
            <button
              onClick={onClose}
              className="w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all"
            >
              로그인으로 돌아가기
            </button>
          ) : step === 'request' ? (
            <form onSubmit={handleRequestCode} className="space-y-4">
              <div>
                <label htmlFor="forgot-email" className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-1">
                  이메일
                </label>
                <input
                  id="forgot-email"
                  type="email"
                  required
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="user@example.com"
                  className={inputClass}
                />
              </div>
              <button
                type="submit"
                disabled={submitting || !email.trim()}
                className="w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {submitting ? '발송 중...' : '인증 코드 받기'}
              </button>
            </form>
          ) : (
            <form onSubmit={handleConfirm} className="space-y-4">
              <div>
                <label htmlFor="forgot-code" className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-1">
                  인증 코드
                </label>
                <input
                  id="forgot-code"
                  type="text"
                  required
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  placeholder="이메일로 받은 코드"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="forgot-new-password" className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-1">
                  새 비밀번호
                </label>
                <input
                  id="forgot-new-password"
                  type="password"
                  required
                  value={newPassword}
                  onChange={(e) => setNewPassword(e.target.value)}
                  placeholder="8자 이상 입력하세요"
                  className={inputClass}
                />
              </div>
              <div>
                <label htmlFor="forgot-confirm-password" className="block text-sm font-medium text-slate-700 dark:text-text-secondary mb-1">
                  새 비밀번호 확인
                </label>
                <input
                  id="forgot-confirm-password"
                  type="password"
                  required
                  value={confirmNewPassword}
                  onChange={(e) => setConfirmNewPassword(e.target.value)}
                  placeholder="비밀번호를 다시 입력하세요"
                  className={inputClass}
                />
              </div>
              <button
                type="submit"
                disabled={submitting}
                className="w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {submitting ? '변경 중...' : '비밀번호 변경'}
              </button>
              <button
                type="button"
                onClick={() => setStep('request')}
                className="w-full text-sm text-slate-500 dark:text-text-muted hover:text-slate-700 dark:hover:text-white transition-colors"
              >
                인증 코드 다시 받기
              </button>
            </form>
          )}
        </div>
      </div>
    </div>
  );
}

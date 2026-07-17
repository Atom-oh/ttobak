'use client';

import { useState } from 'react';
import { useAuth } from './AuthProvider';
import { PrimaryButton } from '@/components/ui/Button';
import { isNewPasswordRequired, NewPasswordRequiredResult } from '@/lib/auth';

interface LoginFormProps {
  onSwitchToSignUp?: () => void;
  onForgotPassword?: () => void;
}

export function LoginForm({ onSwitchToSignUp, onForgotPassword }: LoginFormProps) {
  const { login, completeNewPassword } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [challenge, setChallenge] = useState<NewPasswordRequiredResult | null>(null);
  const [newPassword, setNewPassword] = useState('');
  const [confirmNewPassword, setConfirmNewPassword] = useState('');

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setIsLoading(true);

    try {
      const result = await login(email, password);
      if (isNewPasswordRequired(result)) {
        setChallenge(result);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed');
    } finally {
      setIsLoading(false);
    }
  };

  const handleCompleteNewPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (newPassword !== confirmNewPassword) {
      setError('비밀번호가 일치하지 않습니다');
      return;
    }
    if (newPassword.length < 8) {
      setError('비밀번호는 8자 이상이어야 합니다');
      return;
    }

    setIsLoading(true);
    try {
      await completeNewPassword(challenge!, newPassword);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to set new password');
    } finally {
      setIsLoading(false);
    }
  };

  if (challenge) {
    return (
      <div className="w-full">
        <div className="text-center mb-6">
          <h2 className="text-xl font-bold text-slate-900 dark:text-primary">
            새 비밀번호 설정
          </h2>
          <p className="text-slate-600 dark:text-text-secondary mt-1 text-sm">
            최초 로그인입니다. 계속하려면 새 비밀번호를 설정해주세요
          </p>
        </div>

        <form onSubmit={handleCompleteNewPassword} className="space-y-5">
          {error && (
            <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
              <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
            </div>
          )}

          <div className="space-y-1.5">
            <label
              htmlFor="new-password"
              className="block text-sm font-medium text-slate-700"
            >
              새 비밀번호
            </label>
            <div className="relative group">
              <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-text-muted group-focus-within:text-primary dark:group-focus-within:text-primary transition-colors text-lg">
                lock
              </span>
              <input
                id="new-password"
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                required
                className="w-full pl-10 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-text-muted/40 dark:focus:ring-primary/30"
                placeholder="8자 이상 입력하세요"
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <label
              htmlFor="confirm-new-password"
              className="block text-sm font-medium text-slate-700"
            >
              새 비밀번호 확인
            </label>
            <div className="relative group">
              <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-text-muted group-focus-within:text-primary dark:group-focus-within:text-primary transition-colors text-lg">
                lock_reset
              </span>
              <input
                id="confirm-new-password"
                type="password"
                value={confirmNewPassword}
                onChange={(e) => setConfirmNewPassword(e.target.value)}
                required
                className="w-full pl-10 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-text-muted/40 dark:focus:ring-primary/30"
                placeholder="비밀번호를 다시 입력하세요"
              />
            </div>
          </div>

          <button
            type="submit"
            disabled={isLoading}
            className="w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed dark:bg-primary dark:text-white"
          >
            {isLoading ? '설정 중...' : '비밀번호 설정하고 로그인'}
          </button>
        </form>
      </div>
    );
  }

  return (
    <div className="w-full">
      
      <div className="text-center mb-6">
        <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">로그인</h2>
        <p className="text-slate-600 dark:text-text-secondary mt-1 text-sm">이메일과 비밀번호를 입력해주세요</p>
      </div>

      <form onSubmit={handleSubmit} className="space-y-5">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
            <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
          </div>
        )}

        {/* Email */}
        <div className="space-y-1.5">
          <label
            htmlFor="login-email"
            className="block text-sm font-medium text-slate-700 dark:text-text-secondary"
          >
            이메일
          </label>
          <div className="relative group">
            <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-text-muted group-focus-within:text-primary transition-colors text-lg">
              mail
            </span>
            <input
              id="login-email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              className="w-full pl-10 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-text-muted/40 dark:focus:ring-primary/30"
              placeholder="you@example.com"
            />
          </div>
        </div>

        {/* Password */}
        <div className="space-y-1.5">
          <div className="flex justify-between items-center">
            <label
              htmlFor="login-password"
              className="block text-sm font-medium text-slate-700 dark:text-text-secondary"
            >
              비밀번호
            </label>
            {onForgotPassword && (
              <button
                type="button"
                onClick={onForgotPassword}
                className="text-sm text-primary hover:underline"
              >
                Forgot Password?
              </button>
            )}
          </div>
          <div className="relative group">
            <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-text-muted group-focus-within:text-primary transition-colors text-lg">
              lock
            </span>
            <input
              id="login-password"
              type={showPassword ? 'text' : 'password'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              className="w-full pl-10 pr-12 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-text-muted/40 dark:focus:ring-primary/30"
              placeholder="비밀번호를 입력하세요"
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600 dark:text-text-muted dark:hover:text-white transition-colors"
            >
              <span className="material-symbols-outlined text-xl">
                {showPassword ? 'visibility_off' : 'visibility'}
              </span>
            </button>
          </div>
        </div>

        {/* Submit */}
        <PrimaryButton type="submit" disabled={isLoading}>
          {isLoading ? '로그인 중...' : '로그인'}
        </PrimaryButton>
      </form>

      
      {onSwitchToSignUp && (
        <p className="text-center mt-6 text-slate-600 dark:text-text-muted text-sm">
          계정이 없으신가요?{' '}
          <button
            onClick={onSwitchToSignUp}
            className="text-primary font-semibold hover:underline"
          >
            회원가입
          </button>
        </p>
      )}
    </div>
  );
}

'use client';

import { useState } from 'react';
import { useAuth } from './AuthProvider';

interface LoginFormProps {
  onSwitchToSignUp?: () => void;
  onForgotPassword?: () => void;
}

export function LoginForm({ onSwitchToSignUp, onForgotPassword }: LoginFormProps) {
  const { login } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [error, setError] = useState('');
  const [isLoading, setIsLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setIsLoading(true);

    try {
      await login(email, password);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed');
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <div className="w-full">
      {/* Light mode heading */}
      <div className="text-center mb-6 dark:hidden">
        <h2 className="text-xl font-bold text-slate-900">로그인</h2>
        <p className="text-slate-600 mt-1 text-sm">이메일과 비밀번호를 입력해주세요</p>
      </div>

      <form onSubmit={handleSubmit} className="space-y-5 dark:space-y-6">
        {error && (
          <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg dark:rounded-xl p-3">
            <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
          </div>
        )}

        {/* Email */}
        <div className="space-y-1.5 dark:space-y-2">
          <label
            htmlFor="login-email"
            className="block text-sm font-medium text-slate-700 dark:font-[var(--font-headline)] dark:text-[#8B949E] dark:text-[13px] dark:font-medium dark:uppercase dark:tracking-wide dark:ml-1"
          >
            <span className="dark:hidden">이메일</span>
            <span className="hidden dark:inline">Email Address</span>
          </label>
          <div className="relative group">
            <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-[#849396] group-focus-within:text-primary dark:group-focus-within:text-[#00E5FF] transition-colors text-lg">
              mail
            </span>
            <input
              id="login-email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              className="w-full pl-10 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:h-12 dark:py-0 dark:text-white dark:placeholder-[#849396]/40 dark:focus:ring-0 dark:focus:border-[#00E5FF] dark:focus:shadow-[0_0_12px_rgba(0,229,255,0.2)]"
              placeholder="you@example.com"
            />
          </div>
        </div>

        {/* Password */}
        <div className="space-y-1.5 dark:space-y-2">
          <div className="flex justify-between items-center dark:px-1">
            <label
              htmlFor="login-password"
              className="block text-sm font-medium text-slate-700 dark:font-[var(--font-headline)] dark:text-[#8B949E] dark:text-[13px] dark:font-medium dark:uppercase dark:tracking-wide"
            >
              <span className="dark:hidden">비밀번호</span>
              <span className="hidden dark:inline">Security Key</span>
            </label>
            {onForgotPassword && (
              <button
                type="button"
                onClick={onForgotPassword}
                className="text-sm text-primary hover:underline dark:font-[var(--font-body)] dark:text-xs dark:text-[#e5b5ff] dark:hover:text-[#f4d9ff]"
              >
                Forgot Password?
              </button>
            )}
          </div>
          <div className="relative group">
            <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-[#849396] group-focus-within:text-primary dark:group-focus-within:text-[#00E5FF] transition-colors text-lg">
              lock
            </span>
            <input
              id="login-password"
              type={showPassword ? 'text' : 'password'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              className="w-full pl-10 pr-12 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:h-12 dark:py-0 dark:text-white dark:placeholder-[#849396]/40 dark:focus:ring-0 dark:focus:border-[#00E5FF] dark:focus:shadow-[0_0_12px_rgba(0,229,255,0.2)]"
              placeholder="비밀번호를 입력하세요"
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-3 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600 dark:text-[#849396] dark:hover:text-white transition-colors"
            >
              <span className="material-symbols-outlined text-xl">
                {showPassword ? 'visibility_off' : 'visibility'}
              </span>
            </button>
          </div>
        </div>

        {/* Submit */}
        <button
          type="submit"
          disabled={isLoading}
          className="w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed dark:bg-[#00E5FF] dark:text-[#001f24] dark:font-[var(--font-headline)] dark:font-bold dark:py-4 dark:rounded-xl dark:shadow-[0_0_20px_rgba(0,229,255,0.4)] dark:hover:scale-[1.02] dark:active:scale-[0.98] dark:text-lg dark:tracking-tight"
        >
          {isLoading ? '로그인 중...' : '로그인'}
        </button>
      </form>

      {/* Forgot Password — dark mode only (below form, above divider) */}
      <div className="hidden dark:flex justify-center mt-4">
        <button
          type="button"
          className="font-[var(--font-body)] text-xs text-[#e5b5ff] hover:text-[#f4d9ff] hover:underline underline-offset-4 decoration-1 transition-colors"
        >
          Forgot Password?
        </button>
      </div>

      {onSwitchToSignUp && (
        <p className="text-center mt-6 dark:mt-10 text-slate-600 dark:text-[#849396] text-sm dark:font-[var(--font-body)]">
          계정이 없으신가요?{' '}
          <button
            onClick={onSwitchToSignUp}
            className="text-primary font-semibold hover:underline dark:text-[#e5b5ff] dark:hover:underline dark:underline-offset-4 dark:decoration-2"
          >
            회원가입
          </button>
        </p>
      )}
    </div>
  );
}

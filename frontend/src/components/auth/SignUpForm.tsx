'use client';

import { useState } from 'react';
import { useAuth } from './AuthProvider';

interface SignUpFormProps {
  onSwitchToLogin?: () => void;
}

/* Shared input class for light + dark mode */
const inputClass =
  'w-full pl-10 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:h-12 dark:py-0 dark:text-white dark:placeholder-[#849396]/40 dark:focus:ring-0 dark:focus:border-[#00E5FF] dark:focus:shadow-[0_0_12px_rgba(0,229,255,0.2)]';

function FormField({
  id,
  label,
  labelEn,
  icon,
  type = 'text',
  value,
  onChange,
  placeholder,
  required = true,
  maxLength,
  extraClass,
}: {
  id: string;
  label: string;
  labelEn: string;
  icon: string;
  type?: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
  required?: boolean;
  maxLength?: number;
  extraClass?: string;
}) {
  return (
    <div className="space-y-1.5 dark:space-y-2">
      <label
        htmlFor={id}
        className="block text-sm font-medium text-slate-700 dark:font-[var(--font-headline)] dark:text-[#8B949E] dark:text-[13px] dark:font-medium dark:uppercase dark:tracking-wide dark:ml-1"
      >
        <span className="dark:hidden">{label}</span>
        <span className="hidden dark:inline">{labelEn}</span>
      </label>
      <div className="relative group">
        <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-[#849396] group-focus-within:text-primary dark:group-focus-within:text-[#00E5FF] transition-colors text-lg">
          {icon}
        </span>
        <input
          id={id}
          type={type}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          required={required}
          maxLength={maxLength}
          className={`${inputClass} ${extraClass || ''}`}
          placeholder={placeholder}
        />
      </div>
    </div>
  );
}

export function SignUpForm({ onSwitchToLogin }: SignUpFormProps) {
  const { register, confirmRegistration, login } = useAuth();
  const [step, setStep] = useState<'register' | 'confirm'>('register');
  const [name, setName] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [isLoading, setIsLoading] = useState(false);

  const handleRegister = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (password !== confirmPassword) {
      setError('비밀번호가 일치하지 않습니다');
      return;
    }

    if (password.length < 8) {
      setError('비밀번호는 8자 이상이어야 합니다');
      return;
    }

    setIsLoading(true);
    try {
      await register(email, password, name);
      setStep('confirm');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Registration failed');
    } finally {
      setIsLoading(false);
    }
  };

  const handleConfirm = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');
    setIsLoading(true);

    try {
      await confirmRegistration(email, code);
      await login(email, password);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Confirmation failed');
    } finally {
      setIsLoading(false);
    }
  };

  const errorBanner = error ? (
    <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg dark:rounded-xl p-3">
      <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
    </div>
  ) : null;

  const submitBtnClass =
    'w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed dark:bg-[#00E5FF] dark:text-[#001f24] dark:font-[var(--font-headline)] dark:font-bold dark:py-4 dark:rounded-xl dark:shadow-[0_0_20px_rgba(0,229,255,0.4)] dark:hover:scale-[1.02] dark:active:scale-[0.98] dark:text-lg dark:tracking-tight';

  /* ──────────── Confirm Step ──────────── */
  if (step === 'confirm') {
    return (
      <div className="w-full">
        <div className="text-center mb-6">
          <h2 className="text-xl font-bold text-slate-900 dark:font-[var(--font-headline)] dark:text-[#00E5FF]">이메일 인증</h2>
          <p className="text-slate-600 dark:text-[#bac9cc] mt-1 text-sm dark:font-[var(--font-body)]">
            {email}로 인증 코드를 보냈습니다
          </p>
        </div>

        <form onSubmit={handleConfirm} className="space-y-5 dark:space-y-6">
          {errorBanner}

          <FormField
            id="confirm-code"
            label="인증 코드"
            labelEn="Verification Code"
            icon="pin"
            value={code}
            onChange={setCode}
            placeholder="000000"
            maxLength={6}
            extraClass="text-center text-2xl tracking-widest dark:text-center dark:text-2xl dark:tracking-widest"
          />

          <button type="submit" disabled={isLoading} className={submitBtnClass}>
            {isLoading ? '인증 중...' : '이메일 인증'}
          </button>
        </form>

        <p className="text-center mt-6 dark:mt-10 text-slate-600 dark:text-[#849396] text-sm dark:font-[var(--font-body)]">
          코드를 받지 못하셨나요?{' '}
          <button className="text-primary font-semibold hover:underline dark:text-[#e5b5ff] dark:underline-offset-4 dark:decoration-2">
            재전송
          </button>
        </p>
      </div>
    );
  }

  /* ──────────── Register Step ──────────── */
  return (
    <div className="w-full">
      {/* Light mode heading */}
      <div className="text-center mb-6 dark:hidden">
        <h2 className="text-xl font-bold text-slate-900">회원가입</h2>
        <p className="text-slate-600 mt-1 text-sm">회의 녹음을 시작해보세요</p>
      </div>

      <form onSubmit={handleRegister} className="space-y-4 dark:space-y-5">
        {errorBanner}

        <FormField id="signup-name" label="이름" labelEn="Name" icon="person" value={name} onChange={setName} placeholder="이름을 입력하세요" required={false} />
        <FormField id="signup-email" label="이메일" labelEn="Email Address" icon="mail" type="email" value={email} onChange={setEmail} placeholder="you@example.com" />
        <FormField id="signup-password" label="비밀번호" labelEn="Password" icon="lock" type="password" value={password} onChange={setPassword} placeholder="8자 이상 입력하세요" />
        <FormField id="signup-confirm" label="비밀번호 확인" labelEn="Confirm Password" icon="lock_reset" type="password" value={confirmPassword} onChange={setConfirmPassword} placeholder="비밀번호를 다시 입력하세요" />

        <button type="submit" disabled={isLoading} className={submitBtnClass}>
          {isLoading ? '가입 중...' : '회원가입'}
        </button>
      </form>

      {onSwitchToLogin && (
        <p className="text-center mt-6 dark:mt-10 text-slate-600 dark:text-[#849396] text-sm dark:font-[var(--font-body)]">
          이미 계정이 있으신가요?{' '}
          <button
            onClick={onSwitchToLogin}
            className="text-primary font-semibold hover:underline dark:text-[#e5b5ff] dark:underline-offset-4 dark:decoration-2"
          >
            로그인
          </button>
        </p>
      )}
    </div>
  );
}

'use client';

import { useState, useEffect } from 'react';
import { useAuth } from './AuthProvider';
import { apiFetch } from '@/lib/api';
import { PrimaryButton } from '@/components/ui/Button';

interface SignUpFormProps {
  onSwitchToLogin?: () => void;
}

/* Shared input class for light + dark mode */
const inputClass =
  'w-full pl-10 pr-4 py-2.5 bg-slate-50 border border-slate-200 rounded-lg text-slate-900 placeholder:text-slate-400 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/40 transition-all dark:bg-black/30 dark:border-white/10 dark:text-white dark:placeholder-text-muted/40 dark:focus:ring-primary/30';

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
    <div className="space-y-1.5">
      <label
        htmlFor={id}
        className="block text-sm font-medium text-slate-700 dark:text-text-secondary"
      >
        {label}
      </label>
      <div className="relative group">
        <span className="material-symbols-outlined absolute left-3 top-1/2 -translate-y-1/2 text-slate-400 dark:text-text-muted group-focus-within:text-primary transition-colors text-lg">
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
  const [allowedDomains, setAllowedDomains] = useState<string[]>([]);
  const [domainsEnforced, setDomainsEnforced] = useState(false);

  useEffect(() => {
    apiFetch<{ domains: string[]; enforced: boolean }>('/api/auth/allowed-domains', { skipAuth: true })
      .then((res) => {
        setAllowedDomains(res.domains || []);
        setDomainsEnforced(res.enforced);
      })
      .catch(() => {});
  }, []);

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

    if (domainsEnforced) {
      const domain = email.split('@')[1]?.toLowerCase();
      if (!domain || !allowedDomains.includes(domain)) {
        setError(`허용된 이메일 도메인: ${allowedDomains.join(', ')}`);
        return;
      }
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
    <div className="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-3">
      <p className="text-red-600 dark:text-red-400 text-sm">{error}</p>
    </div>
  ) : null;

  /* ──────────── Confirm Step ──────────── */
  if (step === 'confirm') {
    return (
      <div className="w-full">
        <div className="text-center mb-6">
          <h2 className="text-xl font-bold text-slate-900 dark:font-headline dark:text-primary">이메일 인증</h2>
          <p className="text-slate-600 dark:text-text-secondary mt-1 text-sm">
            {email}로 인증 코드를 보냈습니다
          </p>
        </div>

        <form onSubmit={handleConfirm} className="space-y-5">
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

          <PrimaryButton type="submit" disabled={isLoading}>
            {isLoading ? '인증 중...' : '이메일 인증'}
          </PrimaryButton>
        </form>

        <p className="text-center mt-6 text-slate-600 dark:text-text-muted text-sm">
          코드를 받지 못하셨나요?{' '}
          <button className="text-primary font-semibold hover:underline dark:text-secondary dark:underline-offset-4 dark:decoration-2">
            재전송
          </button>
        </p>
      </div>
    );
  }

  /* ──────────── Register Step ──────────── */
  return (
    <div className="w-full">
      
      <div className="text-center mb-6">
        <h2 className="text-xl font-bold text-slate-900 dark:text-text-main">회원가입</h2>
        <p className="text-slate-600 dark:text-text-secondary mt-1 text-sm">회의 녹음을 시작해보세요</p>
      </div>

      <form onSubmit={handleRegister} className="space-y-4">
        {errorBanner}

        <FormField id="signup-name" label="이름" labelEn="Name" icon="person" value={name} onChange={setName} placeholder="이름을 입력하세요" required={false} />
        <FormField id="signup-email" label="이메일" labelEn="Email Address" icon="mail" type="email" value={email} onChange={setEmail} placeholder="you@example.com" />
        {domainsEnforced && (
          <p className="text-xs text-slate-500 dark:text-text-muted -mt-2 ml-1">
            허용 도메인: {allowedDomains.join(', ')}
          </p>
        )}
        <FormField id="signup-password" label="비밀번호" labelEn="Password" icon="lock" type="password" value={password} onChange={setPassword} placeholder="8자 이상 입력하세요" />
        <FormField id="signup-confirm" label="비밀번호 확인" labelEn="Confirm Password" icon="lock_reset" type="password" value={confirmPassword} onChange={setConfirmPassword} placeholder="비밀번호를 다시 입력하세요" />

        <PrimaryButton type="submit" disabled={isLoading}>
          {isLoading ? '가입 중...' : '회원가입'}
        </PrimaryButton>
      </form>

      {onSwitchToLogin && (
        <p className="text-center mt-6 text-slate-600 dark:text-text-muted text-sm">
          이미 계정이 있으신가요?{' '}
          <button
            onClick={onSwitchToLogin}
            className="text-primary font-semibold hover:underline dark:text-secondary dark:underline-offset-4 dark:decoration-2"
          >
            로그인
          </button>
        </p>
      )}
    </div>
  );
}

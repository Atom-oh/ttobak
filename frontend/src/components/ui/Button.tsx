import { ButtonHTMLAttributes } from 'react';

// The one button style shared by the auth forms — was duplicated byte-for-byte
// across LoginForm and SignUpForm.
export function PrimaryButton({ className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={`w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed dark:bg-primary dark:text-[#001f24] dark:font-headline dark:font-bold dark:py-4 dark:rounded-xl dark:shadow-[0_0_20px_rgba(0,229,255,0.4)] dark:hover:scale-[1.02] dark:active:scale-[0.98] dark:text-lg dark:tracking-tight ${className}`}
      {...props}
    />
  );
}

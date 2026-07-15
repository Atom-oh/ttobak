import { ButtonHTMLAttributes } from 'react';

// The one button style shared by the auth forms — was duplicated byte-for-byte
// across LoginForm and SignUpForm.
export function PrimaryButton({ className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      className={`w-full bg-primary hover:bg-primary-hover text-white font-semibold py-2.5 rounded-lg transition-all disabled:opacity-50 disabled:cursor-not-allowed ${className}`}
      {...props}
    />
  );
}

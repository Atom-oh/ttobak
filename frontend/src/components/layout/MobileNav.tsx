'use client';

import Link from 'next/link';

const navItems = [
  { href: '/', icon: 'home', label: 'Home' },
  { href: '/record', icon: 'mic', label: 'Record' },
  { href: '/files', icon: 'description', label: 'Files' },
  { href: '/profile', icon: 'person', label: 'Profile' },
];

export function MobileNav({ activePath }: { activePath: string }) {
  return (
    <nav className="fixed bottom-0 left-0 right-0 flex gap-2 border-t border-slate-100 dark:border-slate-800 bg-white/90 dark:bg-slate-900/90 backdrop-blur-md px-4 pb-6 pt-2 z-10 lg:hidden">
      {navItems.map((item) => (
        <Link
          key={item.href}
          href={item.href}
          className={`flex flex-1 flex-col items-center justify-center gap-1 transition-colors ${
            activePath === item.href ? 'text-primary' : 'text-slate-400'
          }`}
        >
          <div className="flex h-8 items-center justify-center">
            <span
              className={`material-symbols-outlined ${
                activePath === item.href ? 'fill-1' : ''
              }`}
            >
              {item.icon}
            </span>
          </div>
          <p className="text-[10px] font-bold uppercase tracking-wider">{item.label}</p>
        </Link>
      ))}
    </nav>
  );
}

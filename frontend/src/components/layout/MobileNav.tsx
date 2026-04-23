'use client';

import Link from 'next/link';

const navItems = [
  { href: '/', icon: 'home', label: 'Home' },
  { href: '/chat', icon: 'smart_toy', label: 'AI' },
  { href: '/record', icon: 'mic', label: 'Record' },
  { href: '/insights', icon: 'insights', label: 'Insights' },
  { href: '/profile', icon: 'person', label: 'Profile' },
];

export function MobileNav({ activePath }: { activePath: string }) {
  return (
    <nav className="fixed bottom-0 left-0 right-0 flex gap-2 border-t border-slate-100 dark:border-white/5 bg-white/90 dark:bg-[#09090E]/90 dark:backdrop-blur-xl backdrop-blur-md px-4 pb-6 pb-safe pt-2 z-10 lg:hidden">
      {navItems.map((item) => (
        <Link
          key={item.href}
          href={item.href}
          className={`flex flex-1 flex-col items-center justify-center gap-1 transition-colors ${
            activePath === item.href ? 'text-primary dark:text-[#00E5FF]' : 'text-slate-400 dark:text-[#849396]'
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

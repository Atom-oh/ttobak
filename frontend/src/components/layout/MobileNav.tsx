'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { BillingInsightButton } from '@/components/layout/BillingInsightButton';
import { NAV_GROUPS, SETTINGS_ITEM } from '@/lib/navigation';

const navItems = [
  { href: '/', icon: 'home', label: 'Home' },
  { href: '/chat', icon: 'smart_toy', label: 'AI' },
  { href: '/record', icon: 'mic', label: 'Record' },
];

// Pages that live in the menu sheet's 지식 group; the 지식 tab lights up on them.
const KNOWLEDGE_PATHS = ['/kb', '/insights', '/insights/research', '/dictionary'];

function SheetGroupLabel({ name }: { name: string }) {
  return <p className="text-[11px] font-bold tracking-wider text-slate-500 dark:text-text-muted">{name}</p>;
}

function SheetLink({ href, icon, label, onNavigate }: { href: string; icon: string; label: string; onNavigate: () => void }) {
  return (
    <Link
      href={href}
      onClick={onNavigate}
      className="min-h-11 px-3.5 rounded-lg border border-slate-200 dark:border-white/10 text-sm text-slate-700 dark:text-text-secondary flex items-center gap-2"
    >
      <span className="material-symbols-outlined text-lg">{icon}</span>
      <span>{label}</span>
    </Link>
  );
}

/** Full menu, grouped like the desktop sidebar, opened from the 지식 tab. */
function MenuSheet({ onClose }: { onClose: () => void }) {
  const sheetRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // Move focus into the sheet and give it back to the opener on close.
    const opener = document.activeElement as HTMLElement | null;
    sheetRef.current?.querySelector<HTMLElement>('a, button')?.focus();
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      opener?.focus();
    };
  }, [onClose]);

  return (
    <>
      <button
        type="button"
        aria-label="메뉴 닫기"
        onClick={onClose}
        className="fixed inset-0 z-20 bg-slate-900/35 lg:hidden"
      />
      <div
        ref={sheetRef}
        role="dialog"
        aria-modal="true"
        aria-label="전체 메뉴"
        className="fixed left-0 right-0 bottom-0 z-30 max-h-[80vh] overflow-y-auto rounded-t-2xl bg-white dark:bg-background-dark px-4 pt-4 pb-28 flex flex-col gap-4 shadow-2xl lg:hidden"
      >
        <div aria-hidden="true" className="self-center w-10 h-1 rounded-full bg-slate-300 dark:bg-white/20" />
        {NAV_GROUPS.map((group) => (
          <div key={group.name || 'top'} className="flex flex-col gap-1.5">
            {group.name && <SheetGroupLabel name={group.name} />}
            <div className="flex flex-wrap gap-1.5">
              {group.items.map((item) => (
                <SheetLink key={item.href} {...item} onNavigate={onClose} />
              ))}
            </div>
          </div>
        ))}
        <BillingInsightButton variant="sheet" groupLabel={<SheetGroupLabel name="고객 분석" />} />
        <div className="flex flex-wrap gap-1.5">
          <SheetLink {...SETTINGS_ITEM} onNavigate={onClose} />
        </div>
      </div>
    </>
  );
}

export function MobileNav({ activePath }: { activePath: string }) {
  const [sheetOpen, setSheetOpen] = useState(false);
  // Stable so MenuSheet's focus effect runs once per open, not on every
  // re-render of a polling page.
  const closeSheet = useCallback(() => setSheetOpen(false), []);
  const knowledgeActive = sheetOpen || KNOWLEDGE_PATHS.includes(activePath);
  const isOn = (href: string) => !sheetOpen && activePath === href;
  const tabClass = (active: boolean) =>
    `flex flex-1 flex-col items-center justify-center gap-1 transition-colors ${
      active ? 'text-primary' : 'text-slate-400 dark:text-text-muted'
    }`;

  return (
    <>
      {sheetOpen && <MenuSheet onClose={closeSheet} />}
      {/* z-10 as before so page sheets (e.g. the recording Q&A sheet at z-40)
          still cover the bar; it rises above its own menu sheet only while open. */}
      <nav className={`fixed bottom-0 left-0 right-0 flex gap-2 border-t border-slate-100 dark:border-white/5 bg-white/90 dark:bg-background-dark/90 dark:backdrop-blur-xl backdrop-blur-md px-4 pb-6 pb-safe pt-2 lg:hidden ${sheetOpen ? 'z-40' : 'z-10'}`}>
        {navItems.map((item) => (
          <Link key={item.href} href={item.href} className={tabClass(isOn(item.href))}>
            <div className="flex h-8 items-center justify-center">
              <span className={`material-symbols-outlined ${isOn(item.href) ? 'fill-1' : ''}`}>{item.icon}</span>
            </div>
            <p className="text-[10px] font-bold uppercase tracking-wider">{item.label}</p>
          </Link>
        ))}
        <button
          type="button"
          aria-expanded={sheetOpen}
          aria-haspopup="dialog"
          onClick={() => setSheetOpen((open) => !open)}
          className={tabClass(knowledgeActive)}
        >
          <div className="flex h-8 items-center justify-center">
            <span className={`material-symbols-outlined ${knowledgeActive ? 'fill-1' : ''}`}>library_books</span>
          </div>
          <p className="text-[10px] font-bold uppercase tracking-wider">지식</p>
        </button>
        <Link href="/profile" className={tabClass(isOn('/profile'))}>
          <div className="flex h-8 items-center justify-center">
            <span className={`material-symbols-outlined ${isOn('/profile') ? 'fill-1' : ''}`}>person</span>
          </div>
          <p className="text-[10px] font-bold uppercase tracking-wider">Profile</p>
        </Link>
      </nav>
    </>
  );
}

'use client';

import { type ReactNode } from 'react';
import { Sidebar } from './Sidebar';
import { MobileNav } from './MobileNav';

interface AppLayoutProps {
  activePath: string;
  children: ReactNode;
  showMobileNav?: boolean;
}

export function AppLayout({ activePath, children, showMobileNav = true }: AppLayoutProps) {
  return (
    <div className="flex min-h-screen">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar activePath={activePath} />
      </div>

      {/* Main Content */}
      <main className="flex-1 flex flex-col overflow-hidden animate-in">
        {children}
      </main>

      {/* Mobile Bottom Nav */}
      {showMobileNav && <MobileNav activePath={activePath} />}
    </div>
  );
}

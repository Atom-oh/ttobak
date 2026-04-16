'use client';

import { type ReactNode } from 'react';
import { Sidebar } from './Sidebar';
import { MobileNav } from './MobileNav';
import { DesktopHeader } from './DesktopHeader';

interface AppLayoutProps {
  activePath: string;
  children: ReactNode;
  showMobileNav?: boolean;
  breadcrumbs?: { label: string; href?: string }[];
  isRecording?: boolean;
}

export function AppLayout({ activePath, children, showMobileNav = true, breadcrumbs, isRecording }: AppLayoutProps) {
  return (
    <div className="flex h-screen overflow-hidden bg-background-light dark:bg-background-dark">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar activePath={activePath} />
      </div>

      {/* Main Content */}
      <main className="flex-1 flex flex-col min-h-0 overflow-x-hidden overflow-y-auto">
        {/* Desktop Header - hidden on mobile */}
        <div className="hidden lg:block">
          <DesktopHeader breadcrumbs={breadcrumbs} isRecording={isRecording} />
        </div>

        {/* Page Content */}
        <div className="flex-1 flex flex-col min-h-0 animate-in">
          {children}
        </div>
      </main>

      {/* Mobile Bottom Nav */}
      {showMobileNav && <MobileNav activePath={activePath} />}
    </div>
  );
}

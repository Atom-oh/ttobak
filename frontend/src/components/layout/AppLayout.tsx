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
    <div className="flex h-dvh overflow-hidden bg-background-light dark:bg-background-dark">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar activePath={activePath} />
      </div>

      {/* Main Content */}
      <main className={`flex-1 flex flex-col min-w-0 min-h-0 overflow-hidden ${showMobileNav ? 'workspace-main-with-nav' : ''}`}>
        {/* Desktop Header - hidden on mobile */}
        <div className="hidden lg:block shrink-0">
          <DesktopHeader activePath={activePath} breadcrumbs={breadcrumbs} isRecording={isRecording} />
        </div>

        {/* Page Content. `relative` makes this scroller the containing block of
            absolutely positioned descendants (`sr-only` labels): with the body as
            their containing block they escape the clip and lengthen the document,
            so the whole window scrolls behind the layout. */}
        <div className="relative flex-1 flex flex-col min-w-0 min-h-0 overflow-x-hidden overflow-y-auto animate-in">
          {children}
        </div>
      </main>

      {/* Mobile Bottom Nav */}
      {showMobileNav && <MobileNav activePath={activePath} />}
    </div>
  );
}

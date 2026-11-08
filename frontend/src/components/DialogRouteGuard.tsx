'use client';

import { useEffect } from 'react';
import { usePathname } from 'next/navigation';
import { dismissDialogs } from '@/lib/dialog';

/** Cancels in-app dialogs when the route changes, so a confirmation never
 * outlives the screen that asked for it (`lib/dialog.ts`). */
export function DialogRouteGuard() {
  const pathname = usePathname();
  useEffect(() => () => dismissDialogs(), [pathname]);
  return null;
}

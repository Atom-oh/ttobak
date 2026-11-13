'use client';

import { useEffect } from 'react';
import { installStrayFileDropGuard } from '@/lib/fileDropGuard';
import { isTauri } from '@/lib/tauri';

/** Mac app only: keeps a file dropped outside a drop zone from replacing the page. */
export function FileDropGuard() {
  useEffect(() => (isTauri() ? installStrayFileDropGuard(window) : undefined), []);
  return null;
}

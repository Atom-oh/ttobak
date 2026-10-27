'use client';

import { useEffect } from 'react';
import { useRouter } from 'next/navigation';

// Files was folded into the Library (billing service design §12): meeting
// attachments are the Library's "미팅 첨부" source. Old links keep working.
export default function FilesPage() {
  const router = useRouter();
  useEffect(() => {
    router.replace('/kb?source=meeting');
  }, [router]);
  return null;
}

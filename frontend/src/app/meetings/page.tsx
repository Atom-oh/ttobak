import { Suspense } from 'react';
import { MeetingsWorkspace } from '@/components/workspace/MeetingsWorkspace';

export default function MeetingsPage() {
  return (
    <Suspense fallback={<p role="status" className="p-6">불러오는 중…</p>}>
      <MeetingsWorkspace />
    </Suspense>
  );
}

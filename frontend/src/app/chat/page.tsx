'use client';

import { Suspense } from 'react';
import { ChatClient } from './ChatClient';

export default function ChatPage() {
  return (
    <Suspense fallback={
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    }>
      <ChatClient />
    </Suspense>
  );
}

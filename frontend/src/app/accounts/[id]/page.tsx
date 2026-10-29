import { Suspense } from 'react';
import AccountDetailClient from '@/components/AccountDetailClient';

export async function generateStaticParams() {
  return [{ id: '_' }];
}

export default async function Page(props: { params: Promise<{ id: string }> }) {
  await props.params;
  // Suspense: AccountDetailClient reads ?tab= via useSearchParams (static export).
  return (
    <Suspense fallback={<div className="min-h-screen flex items-center justify-center"><div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" /></div>}>
      <AccountDetailClient />
    </Suspense>
  );
}

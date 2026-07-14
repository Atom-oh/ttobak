import { DocDetailClient } from '@/components/DocDetailClient';

export async function generateStaticParams() {
  return [{ docId: '_' }];
}

export default async function Page(props: { params: Promise<{ docId: string }> }) {
  await props.params;
  return <DocDetailClient />;
}

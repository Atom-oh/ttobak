import { DocDetailClient } from '@/components/DocDetailClient';

export async function generateStaticParams() {
  return [{ id: '_', docId: '_' }];
}

export default async function Page(props: { params: Promise<{ id: string; docId: string }> }) {
  const { id } = await props.params;
  return <DocDetailClient accountId={id} />;
}

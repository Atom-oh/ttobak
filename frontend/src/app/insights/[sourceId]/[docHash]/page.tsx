import InsightDetailPage from './InsightDetailClient';

export async function generateStaticParams() {
  return [{ sourceId: '_', docHash: '_' }];
}

export default async function Page(props: { params: Promise<{ sourceId: string; docHash: string }> }) {
  await props.params;
  return <InsightDetailPage />;
}

import ResearchDetailPage from './ResearchDetailClient';

export async function generateStaticParams() {
  return [{ researchId: '_' }];
}

export default async function Page(props: { params: Promise<{ researchId: string }> }) {
  await props.params;
  return <ResearchDetailPage />;
}

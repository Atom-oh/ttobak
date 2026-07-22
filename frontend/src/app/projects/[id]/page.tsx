import ProjectDetailClient from '@/components/ProjectDetailClient';

export async function generateStaticParams() {
  return [{ id: '_' }];
}

export default async function Page(props: { params: Promise<{ id: string }> }) {
  await props.params;
  return <ProjectDetailClient />;
}

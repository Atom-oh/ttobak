import MeetingDetailPage from './MeetingDetailClient';

// Required for production static export (output: 'export')
export async function generateStaticParams() {
  return [{ id: '_' }];
}

export default async function Page(props: { params: Promise<{ id: string }> }) {
  await props.params;
  return <MeetingDetailPage />;
}

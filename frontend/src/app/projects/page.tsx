'use client';

import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import ProjectsClient from '@/components/ProjectsClient';

export default function ProjectsPage() {
  const { isLoading, isAuthenticated } = useAuth();
  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }
  if (!isAuthenticated) {
    // Match the existing authenticated-page redirect guard.
    if (typeof window !== 'undefined') window.location.href = '/';
    return null;
  }
  return (
    <AppLayout activePath="/projects">
      <div className="p-4 lg:p-8">
        <ProjectsClient />
      </div>
    </AppLayout>
  );
}

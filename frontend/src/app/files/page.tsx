'use client';

import { useState, useEffect } from 'react';
import Link from 'next/link';
import { useRouter } from 'next/navigation';
import { useAuth } from '@/components/auth/AuthProvider';
import { AppLayout } from '@/components/layout/AppLayout';
import { meetingsApi } from '@/lib/api';
import type { Meeting, Attachment } from '@/types/meeting';

interface FileItem extends Attachment {
  meetingId: string;
  meetingTitle: string;
}

function ImageModal({
  file,
  showOriginal,
  onClose,
  onToggle,
}: {
  file: FileItem;
  showOriginal: boolean;
  onClose: () => void;
  onToggle: () => void;
}) {
  const imageUrl = showOriginal
    ? file.originalUrl || file.url
    : file.processedUrl || file.url;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="relative max-w-4xl max-h-[90vh] m-4"
        onClick={(e) => e.stopPropagation()}
      >
        <button
          onClick={onClose}
          className="absolute -top-12 right-0 text-white hover:text-slate-300 transition-colors"
        >
          <span className="material-symbols-outlined text-3xl">close</span>
        </button>

        <img
          src={imageUrl}
          alt={file.name}
          className="max-w-full max-h-[80vh] rounded-lg shadow-2xl"
        />

        {file.originalUrl && file.processedUrl && (
          <div className="absolute bottom-4 left-1/2 -translate-x-1/2 flex gap-2 bg-black/60 backdrop-blur-md rounded-full p-1">
            <button
              onClick={onToggle}
              className={`px-4 py-2 text-sm font-medium rounded-full transition-colors ${
                showOriginal ? 'bg-white text-black' : 'text-white hover:bg-white/10'
              }`}
            >
              Original
            </button>
            <button
              onClick={onToggle}
              className={`px-4 py-2 text-sm font-medium rounded-full transition-colors ${
                !showOriginal ? 'bg-white text-black' : 'text-white hover:bg-white/10'
              }`}
            >
              AI Enhanced
            </button>
          </div>
        )}

        <div className="absolute bottom-0 left-0 right-0 translate-y-full pt-4 text-center">
          <p className="text-white text-sm">{file.name}</p>
          <Link
            href={`/meeting/${file.meetingId}`}
            className="text-primary text-xs mt-1 hover:underline inline-block"
          >
            {file.meetingTitle}
          </Link>
        </div>
      </div>
    </div>
  );
}

export default function FilesPage() {
  const router = useRouter();
  const { isAuthenticated, isLoading } = useAuth();
  const [files, setFiles] = useState<FileItem[]>([]);
  const [isFetching, setIsFetching] = useState(true);
  const [selectedFile, setSelectedFile] = useState<FileItem | null>(null);
  const [showOriginal, setShowOriginal] = useState(false);
  const [filterType, setFilterType] = useState<'all' | 'image' | 'document' | 'audio'>('all');

  useEffect(() => {
    if (!isLoading && !isAuthenticated) {
      router.push('/');
      return;
    }
    if (isAuthenticated) {
      const fetchFiles = async () => {
        try {
          const result = await meetingsApi.list();
          const allFiles: FileItem[] = [];
          for (const meeting of result.meetings) {
            if (meeting.attachments) {
              for (const att of meeting.attachments) {
                allFiles.push({
                  ...att,
                  meetingId: meeting.meetingId,
                  meetingTitle: meeting.title,
                });
              }
            }
          }
          allFiles.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
          setFiles(allFiles);
        } catch (err) {
          console.error('Failed to fetch files:', err);
        } finally {
          setIsFetching(false);
        }
      };
      fetchFiles();
    }
  }, [isAuthenticated, isLoading, router]);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) return null;

  const filteredFiles = filterType === 'all' ? files : files.filter((f) => f.type === filterType);

  const imageCount = files.filter((f) => f.type === 'image').length;
  const docCount = files.filter((f) => f.type === 'document').length;
  const audioCount = files.filter((f) => f.type === 'audio').length;

  return (
    <AppLayout activePath="/files">
      {/* Mobile Header */}
      <header className="lg:hidden flex items-center bg-white dark:bg-[var(--surface)] px-4 py-4 justify-between border-b border-slate-100 dark:border-white/10 sticky top-0 z-10">
        <div className="flex items-center gap-3">
          <div className="text-primary flex size-10 shrink-0 items-center justify-center bg-primary/10 rounded-lg">
            <span className="material-symbols-outlined">description</span>
          </div>
          <h1 className="text-slate-900 dark:text-[#e4e1e9] dark:font-[var(--font-headline)] text-xl font-bold leading-tight tracking-tight">
            Files
          </h1>
        </div>
        <Link
          href="/profile"
          className="text-slate-500 dark:text-[#849396] p-2 hover:bg-slate-50 dark:hover:bg-white/5 rounded-full transition-colors"
        >
          <span className="material-symbols-outlined">account_circle</span>
        </Link>
      </header>

      {/* Content */}
      <div className="flex-1 overflow-y-auto pb-24 lg:pb-8">
        <div className="p-4 lg:px-16 lg:pt-16 lg:pb-8 w-full">
          {/* Title (Desktop) */}
          <div className="hidden lg:block mb-8">
            <h2 className="text-3xl font-bold tracking-tight lg:text-4xl lg:font-black dark:font-[var(--font-headline)] dark:text-[#e4e1e9]">
              Files
            </h2>
            <p className="text-slate-600 dark:text-[#849396] mt-2">
              All attachments from your meetings.
            </p>
          </div>

          {/* Filter Tabs */}
          <div className="flex gap-2 mb-6 overflow-x-auto">
            {[
              { key: 'all' as const, label: 'All', count: files.length },
              { key: 'image' as const, label: 'Images', count: imageCount },
              { key: 'document' as const, label: 'Documents', count: docCount },
              { key: 'audio' as const, label: 'Audio', count: audioCount },
            ].map((tab) => (
              <button
                key={tab.key}
                onClick={() => setFilterType(tab.key)}
                className={`px-4 py-2 rounded-lg text-sm font-semibold whitespace-nowrap transition-colors ${
                  filterType === tab.key
                    ? 'bg-primary text-white dark:text-[#09090E]'
                    : 'bg-slate-100 dark:bg-white/5 text-slate-600 dark:text-[#849396] hover:bg-slate-200 dark:hover:bg-white/10'
                }`}
              >
                {tab.label} ({tab.count})
              </button>
            ))}
          </div>

          {/* File Grid */}
          {isFetching ? (
            <div className="flex items-center justify-center py-20">
              <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
            </div>
          ) : filteredFiles.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-20 text-center">
              <span className="material-symbols-outlined text-6xl text-slate-300 dark:text-[#849396] mb-4">
                folder_open
              </span>
              <h3 className="text-lg font-bold text-slate-900 dark:text-[#e4e1e9] mb-1">
                No files yet
              </h3>
              <p className="text-sm text-slate-500 dark:text-[#849396] max-w-xs">
                Files will appear here when you add attachments to your meetings.
              </p>
            </div>
          ) : (
            <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-4 gap-4">
              {filteredFiles.map((file) => (
                <div
                  key={file.id}
                  onClick={() => {
                    if (file.type === 'image') {
                      setSelectedFile(file);
                      setShowOriginal(false);
                    }
                  }}
                  className="group relative aspect-video rounded-xl overflow-hidden border border-slate-200 cursor-pointer bg-white hover:bg-slate-50 dark:glass-panel dark:hover:border-[#00E5FF]/30 transition-all"
                >
                  {file.type === 'image' ? (
                    <>
                      <img
                        src={file.thumbnailUrl || file.url}
                        alt={file.name}
                        className="w-full h-full object-cover transition-transform group-hover:scale-105"
                      />
                      <div className="absolute inset-0 bg-gradient-to-t from-black/60 to-transparent opacity-0 group-hover:opacity-100 transition-opacity flex flex-col justify-end p-3">
                        <span className="text-white text-xs font-medium truncate">
                          {file.name}
                        </span>
                        <span className="text-white/70 text-[10px] truncate">
                          {file.meetingTitle}
                        </span>
                      </div>
                      {file.processedUrl && (
                        <div className="absolute top-2 right-2 bg-primary text-white text-[10px] font-bold px-2 py-0.5 rounded-full">
                          AI Enhanced
                        </div>
                      )}
                    </>
                  ) : (
                    <div className="flex flex-col items-center justify-center h-full p-4">
                      <span className="material-symbols-outlined text-3xl text-slate-400 mb-2">
                        {file.type === 'document'
                          ? 'description'
                          : file.type === 'audio'
                          ? 'audio_file'
                          : 'video_file'}
                      </span>
                      <span className="text-xs font-medium text-slate-600 dark:text-[#bac9cc] truncate max-w-full">
                        {file.name}
                      </span>
                      <span className="text-[10px] text-slate-400 dark:text-[#849396] truncate max-w-full mt-1">
                        {file.meetingTitle}
                      </span>
                    </div>
                  )}

                  {file.timestamp && (
                    <div className="absolute bottom-2 left-2 px-2 py-0.5 bg-black/60 backdrop-blur-md rounded text-[10px] font-bold text-white">
                      {file.timestamp}
                    </div>
                  )}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {selectedFile && (
        <ImageModal
          file={selectedFile}
          showOriginal={showOriginal}
          onClose={() => setSelectedFile(null)}
          onToggle={() => setShowOriginal(!showOriginal)}
        />
      )}
    </AppLayout>
  );
}

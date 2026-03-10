'use client';

import { useState, useEffect } from 'react';
import { useParams, useRouter } from 'next/navigation';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { MeetingEditor } from '@/components/MeetingEditor';
import { ShareButton } from '@/components/ShareButton';
import { AttachmentGallery } from '@/components/AttachmentGallery';
import { FileUploader } from '@/components/FileUploader';
import { ExportMenu } from '@/components/ExportMenu';
import { QAPanel } from '@/components/QAPanel';
import { meetingsApi } from '@/lib/api';
import type { Meeting, ActionItem, TranscriptSegment, Attachment, SharedUser } from '@/types/meeting';

function formatDate(dateString: string): string {
  const date = new Date(dateString);
  return date.toLocaleDateString('en-US', {
    month: 'long',
    day: 'numeric',
    year: 'numeric',
  });
}

function formatTime(dateString: string): string {
  const date = new Date(dateString);
  return date.toLocaleTimeString('en-US', {
    hour: 'numeric',
    minute: '2-digit',
    hour12: true,
  });
}

function formatTimestamp(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  const secs = Math.floor(seconds % 60);
  return `${mins}:${secs.toString().padStart(2, '0')}`;
}

function Sidebar() {
  return (
    <aside className="w-64 border-r border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-950 flex flex-col fixed h-full">
      <div className="p-6 flex flex-col gap-8 h-full">
        <div className="flex items-center gap-3">
          <div className="size-10 rounded-lg bg-primary flex items-center justify-center text-white">
            <span className="material-symbols-outlined">record_voice_over</span>
          </div>
          <div className="flex flex-col">
            <h1 className="text-sm font-bold truncate">또박</h1>
            <p className="text-xs text-slate-500">AI Meeting Assistant</p>
          </div>
        </div>

        <nav className="flex flex-col gap-1 flex-1">
          <Link
            href="/"
            className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-900 transition-colors"
          >
            <span className="material-symbols-outlined text-slate-500">home</span>
            <span className="text-sm font-medium">Home</span>
          </Link>
          <Link
            href="/"
            className="flex items-center gap-3 px-3 py-2 rounded-lg bg-primary/10 text-primary"
          >
            <span className="material-symbols-outlined">video_library</span>
            <span className="text-sm font-medium">Meetings</span>
          </Link>
          <Link
            href="/files"
            className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-900 transition-colors"
          >
            <span className="material-symbols-outlined text-slate-500">description</span>
            <span className="text-sm font-medium">Notes</span>
          </Link>
          <Link
            href="/settings"
            className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-900 transition-colors"
          >
            <span className="material-symbols-outlined text-slate-500">settings</span>
            <span className="text-sm font-medium">Settings</span>
          </Link>
        </nav>

        <Link
          href="/record"
          className="w-full bg-primary hover:bg-primary/90 text-white font-semibold py-2.5 rounded-lg flex items-center justify-center gap-2 transition-all"
        >
          <span className="material-symbols-outlined text-sm">add</span>
          <span className="text-sm">New Meeting</span>
        </Link>
      </div>
    </aside>
  );
}

export default function MeetingDetailPage() {
  const params = useParams();
  const router = useRouter();
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const [meeting, setMeeting] = useState<Meeting | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [showUploader, setShowUploader] = useState(false);

  useEffect(() => {
    if (!isAuthenticated || !params.id) return;

    const fetchMeeting = async () => {
      try {
        const data = await meetingsApi.get(params.id as string);
        setMeeting(data as Meeting);
      } catch (err) {
        console.error('Failed to fetch meeting:', err);
      } finally {
        setIsLoading(false);
      }
    };
    fetchMeeting();
  }, [isAuthenticated, params.id]);

  // Polling for in-progress meetings
  useEffect(() => {
    if (!meeting || !['transcribing', 'summarizing'].includes(meeting.status)) return;

    const interval = setInterval(async () => {
      try {
        const data = await meetingsApi.get(meeting.meetingId);
        setMeeting(data as Meeting);
        if (data.status === 'done' || data.status === 'error') {
          clearInterval(interval);
        }
      } catch (err) {
        console.error('Polling failed:', err);
      }
    }, 5000);

    return () => clearInterval(interval);
  }, [meeting?.meetingId, meeting?.status]);

  const handleActionItemToggle = (itemId: string) => {
    if (!meeting) return;
    setMeeting({
      ...meeting,
      actionItems: meeting.actionItems?.map((item) =>
        item.id === itemId ? { ...item, completed: !item.completed } : item
      ),
    });
  };

  const handleShare = (user: SharedUser) => {
    if (!meeting) return;
    setMeeting({
      ...meeting,
      sharedWith: [...(meeting.sharedWith || []), user],
    });
  };

  const handleUnshare = (userId: string) => {
    if (!meeting) return;
    setMeeting({
      ...meeting,
      sharedWith: meeting.sharedWith?.filter((u) => u.userId !== userId),
    });
  };

  if (authLoading || isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
      </div>
    );
  }

  if (!isAuthenticated) {
    router.push('/');
    return null;
  }

  if (!meeting) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <p className="text-slate-500">Meeting not found</p>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen">
      {/* Desktop Sidebar */}
      <div className="hidden lg:block">
        <Sidebar />
      </div>

      {/* Main Content */}
      <main className="flex-1 lg:ml-64 flex flex-col">
        {/* Mobile Header */}
        <header className="lg:hidden sticky top-0 z-10 flex items-center bg-white/90 dark:bg-slate-900/90 backdrop-blur-md p-4 border-b border-slate-200 dark:border-slate-800 justify-between">
          <button onClick={() => router.back()} className="text-slate-700 dark:text-slate-300 flex size-10 items-center justify-center rounded-full hover:bg-slate-100 dark:hover:bg-slate-800">
            <span className="material-symbols-outlined">arrow_back</span>
          </button>
          <h2 className="text-slate-900 dark:text-slate-100 text-sm font-bold flex-1 text-center">Meeting Report</h2>
          <button className="flex size-10 items-center justify-center rounded-full hover:bg-slate-100 dark:hover:bg-slate-800">
            <span className="material-symbols-outlined text-slate-700 dark:text-slate-300">more_horiz</span>
          </button>
        </header>

        {/* Content */}
        <div className="flex-1 p-4 lg:p-8 max-w-5xl mx-auto w-full">
          {/* Breadcrumbs - Desktop */}
          <div className="hidden lg:flex items-center gap-2 text-sm text-slate-500 mb-6">
            <Link href="/" className="hover:text-primary transition-colors">Meetings</Link>
            <span className="material-symbols-outlined text-xs">chevron_right</span>
            <span className="text-slate-900 dark:text-slate-200 font-medium">{meeting.title}</span>
          </div>

          {/* Header Section */}
          <header className="mb-8 lg:mb-10">
            <div className="flex items-center gap-2 mb-3">
              {meeting.tags?.[0] && (
                <span className="bg-slate-900 dark:bg-white text-white dark:text-slate-900 text-[10px] font-bold uppercase tracking-wider px-2 py-0.5 rounded">
                  {meeting.tags[0]}
                </span>
              )}
              <span className="text-slate-400 dark:text-slate-500 text-xs">•</span>
              <p className="text-slate-500 dark:text-slate-400 text-xs font-medium">
                {formatDate(meeting.date)} • {formatTime(meeting.date)}
              </p>
            </div>
            <h1 className="text-2xl lg:text-4xl font-black tracking-tight text-slate-900 dark:text-white mb-4">
              {meeting.title}
            </h1>

            {/* Participants & Actions */}
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div className="flex items-center gap-3">
                <div className="flex -space-x-2">
                  {meeting.participants?.slice(0, 4).map((p) => (
                    <div
                      key={p.id}
                      className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-slate-900 bg-slate-200 flex items-center justify-center text-[10px] font-bold text-slate-500 overflow-hidden"
                    >
                      {p.avatarUrl ? (
                        <img src={p.avatarUrl} alt={p.name} className="w-full h-full object-cover" />
                      ) : (
                        p.initials || p.name?.charAt(0)
                      )}
                    </div>
                  ))}
                  {(meeting.participants?.length || 0) > 4 && (
                    <div className="size-8 lg:size-9 rounded-full border-2 border-white dark:border-slate-900 bg-slate-100 dark:bg-slate-800 flex items-center justify-center text-[10px] font-bold text-slate-500">
                      +{meeting.participants!.length - 4}
                    </div>
                  )}
                </div>
                <p className="text-xs text-slate-500 font-medium hidden lg:block">
                  {meeting.participants?.map((p) => p.name?.split(' ')[0]).slice(0, 3).join(', ')}
                  {(meeting.participants?.length || 0) > 3 && ` and ${meeting.participants!.length - 3} others`}
                </p>
              </div>

              <div className="flex items-center gap-2">
                <ExportMenu meetingId={meeting.meetingId} />
                <ShareButton
                  meetingId={meeting.meetingId}
                  sharedWith={meeting.sharedWith}
                  onShare={handleShare}
                  onUnshare={handleUnshare}
                />
              </div>
            </div>
          </header>

          {/* Processing Status Indicator */}
          {meeting.status !== 'done' && (
            <div className="flex items-center gap-3 p-4 bg-primary/5 border border-primary/20 rounded-xl mb-6">
              <div className="animate-spin rounded-full h-5 w-5 border-2 border-primary border-t-transparent" />
              <span className="text-sm font-medium text-primary">
                {meeting.status === 'transcribing' ? 'Transcribing audio...' :
                 meeting.status === 'summarizing' ? 'Generating summary...' :
                 meeting.status === 'recording' ? 'Recording in progress...' : 'Processing...'}
              </span>
            </div>
          )}

          {/* Core Content Grid */}
          <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 lg:gap-8 mb-8 lg:mb-12">
            {/* AI Summary Box */}
            <div className="lg:col-span-7 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-800 rounded-xl p-5 lg:p-6 shadow-sm">
              <div className="flex items-center gap-2 mb-4 text-primary">
                <span className="material-symbols-outlined">auto_awesome</span>
                <h3 className="font-bold">AI Summary</h3>
              </div>
              <p className="text-slate-600 dark:text-slate-300 leading-relaxed">
                {meeting.summary}
              </p>
            </div>

            {/* Action Items List */}
            <div className="lg:col-span-5 bg-primary/5 dark:bg-primary/10 border border-primary/20 rounded-xl p-5 lg:p-6">
              <div className="flex items-center gap-2 mb-4 text-primary">
                <span className="material-symbols-outlined">check_circle</span>
                <h3 className="font-bold">Action Items</h3>
              </div>
              <div className="space-y-4">
                {meeting.actionItems?.map((item) => (
                  <div key={item.id} className="flex items-start gap-3">
                    <input
                      type="checkbox"
                      checked={item.completed}
                      onChange={() => handleActionItemToggle(item.id)}
                      className="mt-1 rounded border-primary/30 text-primary focus:ring-primary h-4 w-4"
                    />
                    <div className="flex flex-col">
                      <span className={`text-sm font-medium ${item.completed ? 'line-through text-slate-400' : 'text-slate-900 dark:text-white'}`}>
                        {item.text}
                      </span>
                      {item.assignee && (
                        <span className="text-[10px] text-slate-500">
                          Assigned to: @{item.assignee}
                        </span>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            </div>
          </div>

          {/* Attachments Gallery */}
          {meeting.attachments && meeting.attachments.length > 0 && (
            <section className="mb-8 lg:mb-12">
              <AttachmentGallery
                attachments={meeting.attachments}
                onUploadClick={() => setShowUploader(true)}
              />
            </section>
          )}

          {/* Upload Modal */}
          {showUploader && (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 backdrop-blur-sm p-4">
              <div className="bg-white dark:bg-slate-900 rounded-xl p-6 max-w-lg w-full">
                <div className="flex items-center justify-between mb-4">
                  <h3 className="font-bold text-slate-900 dark:text-white">Upload Files</h3>
                  <button onClick={() => setShowUploader(false)} className="text-slate-400 hover:text-slate-600">
                    <span className="material-symbols-outlined">close</span>
                  </button>
                </div>
                <FileUploader
                  meetingId={meeting.meetingId}
                  onUploadComplete={(files) => {
                    console.log('Uploaded:', files);
                    setShowUploader(false);
                  }}
                />
              </div>
            </div>
          )}

          {/* Full Transcription */}
          {meeting.transcription && meeting.transcription.length > 0 && (
            <section className="border-t border-slate-200 dark:border-slate-800 pt-8 lg:pt-12">
              <div className="flex items-center justify-between mb-6 lg:mb-8">
                <h2 className="text-lg lg:text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-white">
                  <span className="material-symbols-outlined">notes</span>
                  Full Transcription
                </h2>
                <div className="flex gap-2">
                  <button className="px-3 py-1.5 rounded-lg border border-slate-200 dark:border-slate-800 text-xs font-semibold flex items-center gap-2 bg-white dark:bg-slate-900">
                    <span className="material-symbols-outlined text-sm">search</span>
                    <span className="hidden sm:inline">Search</span>
                  </button>
                  <button className="px-3 py-1.5 rounded-lg border border-slate-200 dark:border-slate-800 text-xs font-semibold flex items-center gap-2 bg-white dark:bg-slate-900">
                    <span className="material-symbols-outlined text-sm">download</span>
                    <span className="hidden sm:inline">Export</span>
                  </button>
                </div>
              </div>

              <div className="space-y-6 lg:space-y-8">
                {meeting.transcription.map((segment) => (
                  <div key={segment.id} className="flex gap-4 lg:gap-6">
                    <div className="w-12 lg:w-16 pt-1 flex-shrink-0">
                      <span className="text-xs font-bold text-primary px-2 py-1 bg-primary/10 rounded">
                        {formatTimestamp(segment.startTime)}
                      </span>
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 mb-2">
                        <span className="text-sm font-black text-slate-900 dark:text-white">{segment.speaker}</span>
                        <span className="text-[10px] text-slate-400 font-medium">{segment.timestamp}</span>
                      </div>
                      <p className="text-slate-600 dark:text-slate-400 text-sm leading-relaxed">
                        {segment.text}
                      </p>
                    </div>
                  </div>
                ))}
              </div>
            </section>
          )}

          {/* Q&A Panel */}
          <section className="border-t border-slate-200 dark:border-slate-800 pt-8 lg:pt-12">
            <h2 className="text-lg lg:text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-white mb-6">
              <span className="material-symbols-outlined">question_answer</span>
              Meeting Q&A
            </h2>
            <QAPanel meetingId={meeting.meetingId} />
          </section>
        </div>

        {/* Mobile Bottom Nav */}
        <nav className="lg:hidden fixed bottom-0 left-0 right-0 bg-white/95 dark:bg-slate-900/95 backdrop-blur-lg border-t border-slate-200 dark:border-slate-800 px-6 py-4 flex justify-between items-center">
          <Link href="/" className="flex flex-col items-center gap-1 text-slate-400">
            <span className="material-symbols-outlined">home</span>
            <span className="text-[10px] font-bold uppercase tracking-tight">Home</span>
          </Link>
          <Link href="/search" className="flex flex-col items-center gap-1 text-slate-400">
            <span className="material-symbols-outlined">search</span>
            <span className="text-[10px] font-bold uppercase tracking-tight">Search</span>
          </Link>
          <div className="relative -top-6">
            <Link
              href="/record"
              className="bg-slate-900 dark:bg-white size-14 rounded-full shadow-2xl flex items-center justify-center text-white dark:text-slate-900 ring-4 ring-white dark:ring-slate-900"
            >
              <span className="material-symbols-outlined text-3xl">add</span>
            </Link>
          </div>
          <Link href="/notifications" className="flex flex-col items-center gap-1 text-slate-400">
            <span className="material-symbols-outlined">notifications</span>
            <span className="text-[10px] font-bold uppercase tracking-tight">Alerts</span>
          </Link>
          <Link href="/profile" className="flex flex-col items-center gap-1 text-slate-400">
            <span className="material-symbols-outlined">person</span>
            <span className="text-[10px] font-bold uppercase tracking-tight">Profile</span>
          </Link>
        </nav>
      </main>
    </div>
  );
}

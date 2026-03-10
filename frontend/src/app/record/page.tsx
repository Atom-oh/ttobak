'use client';

import { useState, useRef } from 'react';
import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { RecordButton } from '@/components/RecordButton';
import { FileUploader } from '@/components/FileUploader';
import { LiveTranscript } from '@/components/LiveTranscript';
import { meetingsApi, uploadsApi } from '@/lib/api';

interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

interface TranslationEntry {
  text: string;
  targetLang: string;
  timestamp: string;
}

const langNames: Record<string, string> = {
  en: 'English',
  ko: '한국어',
  ja: '日本語',
};

export default function RecordPage() {
  const router = useRouter();
  const { isAuthenticated, isLoading } = useAuth();
  const [meetingTitle, setMeetingTitle] = useState('');
  const [capturedImages, setCapturedImages] = useState<{ name: string; url: string }[]>([]);
  const [isRecording, setIsRecording] = useState(false);

  // New state for API integration
  const [serverMeetingId, setServerMeetingId] = useState<string | null>(null);
  const [recordMode, setRecordMode] = useState<'offline' | 'online'>('offline');
  const [targetLangs, setTargetLangs] = useState<string[]>([]);
  const [transcripts, setTranscripts] = useState<TranscriptEntry[]>([]);
  const [translations, setTranslations] = useState<TranslationEntry[]>([]);
  const wsRef = useRef<WebSocket | null>(null);

  if (isLoading) {
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

  const toggleLang = (lang: string) => {
    setTargetLangs((prev) =>
      prev.includes(lang) ? prev.filter((l) => l !== lang) : [...prev, lang]
    );
  };

  const handleRecordingComplete = async (audioUrl: string) => {
    // Create meeting on server first
    try {
      const result = await meetingsApi.create({
        title: meetingTitle || 'Untitled Meeting',
      });
      const newMeetingId = result.meetingId;
      setServerMeetingId(newMeetingId);

      // The RecordButton already uploads the audio via uploadAudioBlob
      // and returns the URL. We just need to notify the backend.
      // Extract the S3 key from the URL if needed, or the upload lib handles it.
      console.log('Recording uploaded to:', audioUrl);

      // Navigate to meeting detail
      router.push(`/meeting/${newMeetingId}`);
    } catch (err) {
      console.error('Failed to create meeting:', err);
      // Fallback to home
      router.push('/');
    }
  };

  const handleUploadComplete = (files: { name: string; url: string }[]) => {
    setCapturedImages((prev) => [...prev, ...files]);
  };

  // Use client-side meetingId for RecordButton until server creates one
  const clientMeetingId = serverMeetingId || `meeting_${Date.now()}`;

  return (
    <div className="min-h-screen bg-background-light dark:bg-background-dark">
      {/* Header */}
      <header className="flex items-center justify-between px-6 py-4 bg-white/80 dark:bg-slate-900/80 backdrop-blur-md sticky top-0 z-10">
        <button
          onClick={() => router.back()}
          className="p-2 hover:bg-primary/10 rounded-full transition-colors"
        >
          <span className="material-symbols-outlined text-slate-700 dark:text-slate-300">arrow_back</span>
        </button>
        <input
          type="text"
          value={meetingTitle}
          onChange={(e) => setMeetingTitle(e.target.value)}
          placeholder="Meeting Title"
          className="text-lg font-bold tracking-tight bg-transparent border-none text-center focus:outline-none focus:ring-0 text-slate-900 dark:text-white placeholder:text-slate-400 flex-1 mx-4"
        />
        <button className="p-2 hover:bg-primary/10 rounded-full transition-colors text-primary">
          <span className="material-symbols-outlined">settings</span>
        </button>
      </header>

      {/* Main Content */}
      <main className="flex-1 flex flex-col px-6 pt-8 pb-32 max-w-2xl mx-auto">
        {/* Recording Mode Toggle */}
        <div className="flex items-center gap-4 mb-6">
          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="radio"
              name="recordMode"
              checked={recordMode === 'offline'}
              onChange={() => setRecordMode('offline')}
              className="text-primary focus:ring-primary"
            />
            <span className="text-sm font-medium">Offline</span>
          </label>
          <label className="flex items-center gap-2 cursor-pointer">
            <input
              type="radio"
              name="recordMode"
              checked={recordMode === 'online'}
              onChange={() => setRecordMode('online')}
              className="text-primary focus:ring-primary"
            />
            <span className="text-sm font-medium flex items-center gap-1">
              <span className="material-symbols-outlined text-sm">stream</span>
              Realtime
            </span>
          </label>
        </div>

        {/* Translation Language Selector (shown in online mode) */}
        {recordMode === 'online' && (
          <div className="flex flex-wrap gap-2 mb-4">
            <span className="text-xs font-bold text-slate-500 uppercase tracking-wider">Translate to:</span>
            {['en', 'ko', 'ja'].map((lang) => (
              <label key={lang} className="flex items-center gap-1.5 px-2 py-1 rounded-lg bg-slate-100 dark:bg-slate-800 cursor-pointer">
                <input
                  type="checkbox"
                  checked={targetLangs.includes(lang)}
                  onChange={() => toggleLang(lang)}
                  className="rounded text-primary focus:ring-primary h-3.5 w-3.5"
                />
                <span className="text-xs font-medium">{langNames[lang]}</span>
              </label>
            ))}
          </div>
        )}

        {/* Recording Section */}
        <div className="flex flex-col items-center justify-center mb-12">
          <RecordButton
            meetingId={clientMeetingId}
            meetingTitle={meetingTitle || 'Untitled Meeting'}
            onRecordingComplete={handleRecordingComplete}
            onError={(error) => console.error(error)}
          />
        </div>

        {/* Live Transcript (shown in online mode while recording) */}
        {recordMode === 'online' && isRecording && (
          <LiveTranscript transcripts={transcripts} translations={translations} />
        )}

        {/* Captured Images Section */}
        <section className="mt-8">
          <div className="flex items-center justify-between mb-4">
            <h2 className="text-base font-bold text-slate-800 dark:text-slate-200 uppercase tracking-wider text-xs">
              Captured Images
            </h2>
            {capturedImages.length > 0 && (
              <button className="text-primary text-sm font-semibold">
                View All ({capturedImages.length})
              </button>
            )}
          </div>

          <div className="grid grid-cols-3 gap-3">
            {capturedImages.slice(0, 2).map((img, index) => (
              <div key={index} className="relative group cursor-pointer">
                <div className="aspect-square bg-slate-200 dark:bg-slate-800 rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700">
                  <img
                    src={img.url}
                    alt={img.name}
                    className="w-full h-full object-cover"
                  />
                </div>
              </div>
            ))}

            {/* Add File Button */}
            <label className="relative cursor-pointer">
              <div className="aspect-square bg-slate-200 dark:bg-slate-800 rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700 flex flex-col items-center justify-center bg-primary/5 border-dashed hover:bg-primary/10 transition-colors">
                <span className="material-symbols-outlined text-primary/40 text-3xl">upload_file</span>
                <span className="text-[10px] font-medium text-primary/60 mt-1">Add File</span>
              </div>
              <input
                type="file"
                accept="image/*"
                multiple
                className="hidden"
                onChange={(e) => {
                  if (e.target.files) {
                    // In production, upload to S3
                    const files = Array.from(e.target.files).map((f) => ({
                      name: f.name,
                      url: URL.createObjectURL(f),
                    }));
                    setCapturedImages((prev) => [...prev, ...files]);
                  }
                }}
              />
            </label>
          </div>
        </section>

        {/* Drag and Drop Uploader (Desktop) */}
        <section className="hidden lg:block mt-8">
          <FileUploader
            meetingId={clientMeetingId}
            onUploadComplete={handleUploadComplete}
            accept="image/*"
          />
        </section>
      </main>

      {/* Bottom Navigation - Mobile */}
      <nav className="lg:hidden fixed bottom-0 w-full bg-white dark:bg-slate-900 border-t border-slate-100 dark:border-slate-800 px-6 pb-6 pt-3 flex justify-between items-center z-20">
        <Link
          href="/"
          className="flex flex-col items-center gap-1 text-slate-400 dark:text-slate-500 hover:text-primary transition-colors"
        >
          <span className="material-symbols-outlined">video_camera_front</span>
          <span className="text-xs font-medium">Meetings</span>
        </Link>
        <div className="flex flex-col items-center gap-1 text-primary">
          <span className="material-symbols-outlined">mic</span>
          <span className="text-xs font-medium">Record</span>
        </div>
        <Link
          href="/files"
          className="flex flex-col items-center gap-1 text-slate-400 dark:text-slate-500 hover:text-primary transition-colors"
        >
          <span className="material-symbols-outlined">description</span>
          <span className="text-xs font-medium">Files</span>
        </Link>
        <Link
          href="/profile"
          className="flex flex-col items-center gap-1 text-slate-400 dark:text-slate-500 hover:text-primary transition-colors"
        >
          <span className="material-symbols-outlined">person</span>
          <span className="text-xs font-medium">Profile</span>
        </Link>
      </nav>
    </div>
  );
}

'use client';

import { useState, useRef, useCallback } from 'react';
import { useRouter } from 'next/navigation';
import Link from 'next/link';
import { useAuth } from '@/components/auth/AuthProvider';
import { RecordButton } from '@/components/RecordButton';
import { FileUploader } from '@/components/FileUploader';
import { LiveTranscript } from '@/components/LiveTranscript';
import { meetingsApi } from '@/lib/api';
import { BrowserSpeechRecognition, countWords } from '@/lib/speechRecognition';

interface TranscriptEntry {
  text: string;
  isFinal: boolean;
  timestamp: string;
}

type PostRecordingStep = 'uploading' | 'creating' | 'processing' | 'redirecting' | 'error';

export default function RecordPage() {
  const router = useRouter();
  const { isAuthenticated, isLoading } = useAuth();
  const [meetingTitle, setMeetingTitle] = useState('');
  const [capturedImages, setCapturedImages] = useState<{ name: string; url: string }[]>([]);

  // Recording state
  const [isRecording, setIsRecording] = useState(false);
  const [isPaused, setIsPaused] = useState(false);

  // Speech recognition state
  const [transcripts, setTranscripts] = useState<TranscriptEntry[]>([]);
  const [currentInterim, setCurrentInterim] = useState<string>('');
  const [totalWordCount, setTotalWordCount] = useState(0);
  const lastSummaryWordCountRef = useRef(0);
  const speechRef = useRef<BrowserSpeechRecognition | null>(null);

  // Post-recording state
  const [postRecordingStep, setPostRecordingStep] = useState<PostRecordingStep | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  // Server meeting ID
  const [serverMeetingId, setServerMeetingId] = useState<string | null>(null);

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

  const startSpeechRecognition = () => {
    if (!BrowserSpeechRecognition.isSupported()) return;

    const speech = new BrowserSpeechRecognition('ko-KR');
    speechRef.current = speech;

    speech.start((result) => {
      if (result.isFinal) {
        setTranscripts((prev) => [...prev, result]);
        setCurrentInterim('');

        // Track word count
        const words = countWords(result.text);
        setTotalWordCount((prev) => {
          const newTotal = prev + words;
          // Log when crossing 1000-word threshold (future: trigger interim summary)
          if (newTotal - lastSummaryWordCountRef.current >= 1000) {
            console.log(`Word count threshold reached: ${newTotal} words`);
            lastSummaryWordCountRef.current = newTotal;
          }
          return newTotal;
        });
      } else {
        setCurrentInterim(result.text);
      }
    });
  };

  const handleRecordingStart = () => {
    setIsRecording(true);
    setIsPaused(false);
    setTranscripts([]);
    setCurrentInterim('');
    setTotalWordCount(0);
    lastSummaryWordCountRef.current = 0;
    startSpeechRecognition();
  };

  const handleRecordingPause = () => {
    setIsPaused(true);
    speechRef.current?.pause();
  };

  const handleRecordingResume = () => {
    setIsPaused(false);
    speechRef.current?.resume();
  };

  const handleRecordingStop = () => {
    setIsRecording(false);
    setIsPaused(false);
    speechRef.current?.stop();
    speechRef.current = null;
  };

  const handleRecordingComplete = async (audioUrl: string) => {
    setPostRecordingStep('uploading');

    try {
      setPostRecordingStep('creating');
      const result = await meetingsApi.create({
        title: meetingTitle || 'Untitled Meeting',
      });
      const newMeetingId = result.meetingId;
      setServerMeetingId(newMeetingId);

      console.log('Recording uploaded to:', audioUrl);

      setPostRecordingStep('processing');
      // Brief delay to show processing state before redirect
      await new Promise((resolve) => setTimeout(resolve, 1500));

      setPostRecordingStep('redirecting');
      router.push(`/meeting/${newMeetingId}`);
    } catch (err) {
      console.error('Failed to create meeting:', err);
      setErrorMessage(err instanceof Error ? err.message : 'Failed to create meeting');
      setPostRecordingStep('error');
    }
  };

  const handleRetry = () => {
    setPostRecordingStep(null);
    setErrorMessage(null);
  };

  const handleUploadComplete = (files: { name: string; url: string }[]) => {
    setCapturedImages((prev) => [...prev, ...files]);
  };

  // Combine final transcripts + current interim for display
  const displayTranscripts: TranscriptEntry[] = [
    ...transcripts,
    ...(currentInterim
      ? [{ text: currentInterim, isFinal: false, timestamp: new Date().toISOString() }]
      : []),
  ];

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
        {/* Recording Section */}
        <div className="flex flex-col items-center justify-center mb-8">
          <RecordButton
            meetingId={clientMeetingId}
            meetingTitle={meetingTitle || 'Untitled Meeting'}
            onRecordingComplete={handleRecordingComplete}
            onError={(error) => {
              setErrorMessage(error);
              setPostRecordingStep('error');
            }}
            onRecordingStart={handleRecordingStart}
            onRecordingPause={handleRecordingPause}
            onRecordingResume={handleRecordingResume}
            onRecordingStop={handleRecordingStop}
          />
        </div>

        {/* Live Transcript — always shown when recording */}
        {isRecording && (
          <LiveTranscript
            transcripts={displayTranscripts}
            wordCount={totalWordCount}
          />
        )}

        {/* Captured Images Section */}
        {!isRecording && !postRecordingStep && (
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
        )}

        {/* Drag and Drop Uploader (Desktop) */}
        {!isRecording && !postRecordingStep && (
          <section className="hidden lg:block mt-8">
            <FileUploader
              meetingId={clientMeetingId}
              onUploadComplete={handleUploadComplete}
              accept="image/*"
            />
          </section>
        )}
      </main>

      {/* Post-Recording Processing Overlay */}
      {postRecordingStep && (
        <div className="fixed inset-0 z-50 bg-white/90 dark:bg-slate-900/90 backdrop-blur-sm flex items-center justify-center">
          <div className="flex flex-col items-center text-center px-8 max-w-sm">
            {postRecordingStep === 'error' ? (
              <>
                <div className="w-16 h-16 rounded-full bg-red-100 dark:bg-red-900/30 flex items-center justify-center mb-4">
                  <span className="material-symbols-outlined text-red-500 text-3xl">error</span>
                </div>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-2">
                  Something went wrong
                </h3>
                <p className="text-sm text-slate-500 dark:text-slate-400 mb-6">
                  {errorMessage || 'An unexpected error occurred.'}
                </p>
                <div className="flex gap-3">
                  <button
                    onClick={handleRetry}
                    className="px-6 py-2.5 rounded-lg border border-slate-200 dark:border-slate-700 text-sm font-medium text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors"
                  >
                    Try Again
                  </button>
                  <button
                    onClick={() => router.push('/')}
                    className="px-6 py-2.5 rounded-lg bg-primary text-white text-sm font-medium hover:bg-primary/90 transition-colors"
                  >
                    Go to Home
                  </button>
                </div>
              </>
            ) : (
              <>
                <div className="w-16 h-16 rounded-full bg-primary/10 flex items-center justify-center mb-4">
                  <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
                </div>
                <h3 className="text-lg font-bold text-slate-900 dark:text-white mb-2">
                  {postRecordingStep === 'uploading' && 'Uploading audio...'}
                  {postRecordingStep === 'creating' && 'Creating meeting...'}
                  {postRecordingStep === 'processing' && 'Processing recording...'}
                  {postRecordingStep === 'redirecting' && 'Opening meeting...'}
                </h3>
                <p className="text-sm text-slate-500 dark:text-slate-400">
                  {postRecordingStep === 'uploading' && 'Sending your recording to the server.'}
                  {postRecordingStep === 'creating' && 'Setting up your meeting notes.'}
                  {postRecordingStep === 'processing' && 'AI transcription and summary will be ready shortly.'}
                  {postRecordingStep === 'redirecting' && 'Taking you to your meeting...'}
                </p>
                {/* Progress dots */}
                <div className="flex gap-2 mt-6">
                  {(['uploading', 'creating', 'processing', 'redirecting'] as const).map((step, i) => {
                    const currentIdx = ['uploading', 'creating', 'processing', 'redirecting'].indexOf(postRecordingStep);
                    const stepIdx = i;
                    return (
                      <div
                        key={step}
                        className={`w-2 h-2 rounded-full transition-all duration-300 ${
                          stepIdx <= currentIdx
                            ? 'bg-primary'
                            : 'bg-slate-200 dark:bg-slate-700'
                        } ${stepIdx === currentIdx ? 'scale-125' : ''}`}
                      />
                    );
                  })}
                </div>
              </>
            )}
          </div>
        </div>
      )}

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

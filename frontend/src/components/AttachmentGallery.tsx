'use client';

import { useState } from 'react';
import type { Attachment } from '@/types/meeting';
import { DocumentAttachmentCard } from './meeting/DocumentAttachmentCard';

interface AttachmentGalleryProps {
  meetingId: string;
  canEdit: boolean;
  summaryRevision?: number;
  attachments: Attachment[];
  onUploadClick?: () => void;
  onResummarize?: () => void;
  resummaryDisabled?: boolean;
}

function ImageModal({
  attachment,
  showOriginal,
  onClose,
  onToggle,
}: {
  attachment: Attachment;
  showOriginal: boolean;
  onClose: () => void;
  onToggle: () => void;
}) {
  const imageUrl = showOriginal
    ? attachment.originalUrl || attachment.url
    : attachment.processedUrl || attachment.url;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 backdrop-blur-sm"
      onClick={onClose}
    >
      <div
        className="relative max-w-4xl max-h-[90vh] m-4"
        onClick={(e) => e.stopPropagation()}
      >
        {/* Close Button */}
        <button
          onClick={onClose}
          className="absolute -top-12 right-0 text-white hover:text-slate-300 transition-colors"
        >
          <span className="material-symbols-outlined text-3xl">close</span>
        </button>

        {/* Image */}
        <img
          src={imageUrl}
          alt={attachment.name}
          className="max-w-full max-h-[80vh] rounded-lg shadow-2xl"
        />

        {/* Toggle Controls */}
        {attachment.originalUrl && attachment.processedUrl && (
          <div className="absolute bottom-4 left-1/2 -translate-x-1/2 flex gap-2 bg-black/60 backdrop-blur-md rounded-full p-1">
            <button
              onClick={onToggle}
              className={`px-4 py-2 text-sm font-medium rounded-full transition-colors ${
                showOriginal
                  ? 'bg-white text-black'
                  : 'text-white hover:bg-white/10'
              }`}
            >
              Original
            </button>
            <button
              onClick={onToggle}
              className={`px-4 py-2 text-sm font-medium rounded-full transition-colors ${
                !showOriginal
                  ? 'bg-white text-black'
                  : 'text-white hover:bg-white/10'
              }`}
            >
              AI Enhanced
            </button>
          </div>
        )}

        {/* Info Bar */}
        <div className="absolute bottom-0 left-0 right-0 translate-y-full pt-4 text-center">
          <p className="text-white text-sm">{attachment.name}</p>
          {attachment.timestamp && (
            <p className="text-slate-400 text-xs mt-1">Captured at {attachment.timestamp}</p>
          )}
        </div>
      </div>
    </div>
  );
}

function AttachmentCard({
  attachment,
  onClick,
}: {
  attachment: Attachment;
  onClick: () => void;
}) {
  const isImage = attachment.type === 'image';
  const thumbnailUrl = attachment.thumbnailUrl || attachment.url;

  return (
    <div
      onClick={onClick}
      className="group relative aspect-video rounded-xl overflow-hidden border border-slate-200 dark:border-white/10 cursor-pointer bg-white dark:bg-surface-lowest transition-all duration-300 hover:shadow-lg"
    >
      {isImage ? (
        <>
          <img
            src={thumbnailUrl}
            alt={attachment.name}
            className="w-full h-full object-cover transition-transform duration-300 group-hover:scale-105"
          />
          <div className="absolute inset-0 bg-gradient-to-t from-black/60 to-transparent opacity-0 group-hover:opacity-100 transition-opacity duration-300 flex items-end p-3">
            <span className="text-white text-xs font-medium truncate">
              {attachment.name}
            </span>
          </div>
          {attachment.processedUrl && (
            <div className="absolute top-2 right-2 bg-primary text-white text-[10px] font-bold px-2 py-0.5 rounded-full">
              AI Enhanced
            </div>
          )}
        </>
      ) : (
        <div className="flex flex-col items-center justify-center h-full p-4">
          <span className="material-symbols-outlined text-3xl text-slate-400 mb-2">
            {attachment.type === 'document'
              ? 'description'
              : attachment.type === 'audio'
              ? 'audio_file'
              : 'video_file'}
          </span>
          <span className="text-xs font-medium text-slate-600 dark:text-slate-400 truncate max-w-full">
            {attachment.name}
          </span>
        </div>
      )}

      {/* Timestamp Badge */}
      {attachment.timestamp && (
        <div className="absolute bottom-2 left-2 px-2 py-0.5 bg-black/60 backdrop-blur-md rounded text-[10px] font-bold text-white">
          {attachment.timestamp}
        </div>
      )}
    </div>
  );
}

export function AttachmentGallery({
  meetingId,
  canEdit,
  summaryRevision = 0,
  attachments,
  onUploadClick,
  onResummarize,
  resummaryDisabled,
}: AttachmentGalleryProps) {
  const [selectedAttachment, setSelectedAttachment] = useState<Attachment | null>(null);
  const [showOriginal, setShowOriginal] = useState(false);

  const handleCardClick = (attachment: Attachment) => {
    if (attachment.type === 'image') {
      setSelectedAttachment(attachment);
      setShowOriginal(false);
    } else if (attachment.url) {
      // Documents/audio/video: open the file in a new tab (presigned S3 URL).
      window.open(attachment.url, '_blank', 'noopener,noreferrer');
    }
  };

  return (
    <div>
      {/* Header */}
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-bold flex items-center gap-2 text-slate-900 dark:text-white">
          <span className="material-symbols-outlined">attachment</span>
          첨부 파일 ({attachments.length})
        </h2>
        {onUploadClick && <button
          type="button"
          onClick={onUploadClick}
          className="text-sm font-semibold text-primary hover:underline"
        >
          파일 추가
        </button>}
      </div>

      {/* Grid */}
      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-4">
        {attachments.map((attachment) => (
          attachment.type === 'document' ? (
            <DocumentAttachmentCard
              key={`${meetingId}:${attachment.id}:${attachment.originalKey ?? ''}`}
              meetingId={meetingId}
              attachment={attachment}
              canEdit={canEdit}
              summaryRevision={summaryRevision}
              onResummarize={onResummarize}
              resummaryDisabled={resummaryDisabled}
            />
          ) : <AttachmentCard
            key={`${meetingId}:${attachment.id}`}
            attachment={attachment}
            onClick={() => handleCardClick(attachment)}
          />
        ))}

        {/* Upload Placeholder */}
        {onUploadClick && (
          <button
            type="button"
            onClick={onUploadClick}
            className="flex flex-col items-center justify-center aspect-video rounded-xl border-2 border-dashed border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900/50 hover:border-primary hover:bg-slate-50 dark:hover:bg-slate-800/50 transition-all cursor-pointer group"
          >
            <span className="material-symbols-outlined text-2xl text-slate-400 group-hover:text-primary mb-1">
              add_circle
            </span>
            <span className="text-[10px] font-bold text-slate-500 group-hover:text-primary uppercase tracking-wider">
              파일 첨부
            </span>
          </button>
        )}
      </div>

      {/* Image Modal */}
      {selectedAttachment && (
        <ImageModal
          attachment={selectedAttachment}
          showOriginal={showOriginal}
          onClose={() => setSelectedAttachment(null)}
          onToggle={() => setShowOriginal(!showOriginal)}
        />
      )}
    </div>
  );
}

export interface Meeting {
  PK: string;
  SK: string;
  meetingId: string;
  userId: string;
  title: string;
  description?: string;
  date: string;
  duration?: number;
  status: 'recording' | 'transcribing' | 'summarizing' | 'done' | 'error';
  tags?: string[];
  participants?: Participant[];
  summary?: string;
  actionItems?: ActionItem[];
  transcription?: TranscriptSegment[];
  attachments?: Attachment[];
  sharedWith?: SharedUser[];
  createdAt: string;
  updatedAt: string;
}

export interface Participant {
  id: string;
  name: string;
  email?: string;
  avatarUrl?: string;
  initials?: string;
}

export interface ActionItem {
  id: string;
  text: string;
  completed: boolean;
  assignee?: string;
  dueDate?: string;
}

export interface TranscriptSegment {
  id: string;
  speaker: string;
  speakerInitials?: string;
  timestamp: string;
  text: string;
  startTime: number;
  endTime: number;
}

export interface Attachment {
  id: string;
  name: string;
  type: 'image' | 'document' | 'audio' | 'video';
  url: string;
  thumbnailUrl?: string;
  originalUrl?: string;
  processedUrl?: string;
  size?: number;
  timestamp?: string;
  createdAt: string;
}

export interface SharedUser {
  userId: string;
  email: string;
  name?: string;
  permission: 'read' | 'edit';
  sharedAt: string;
}

export interface TranscriptComparison {
  meetingId: string;
  providerA: {
    name: string;
    segments: TranscriptSegment[];
  };
  providerB: {
    name: string;
    segments: TranscriptSegment[];
  };
}

export interface MeetingListFilter {
  tab: 'all' | 'recent' | 'shared' | 'favorites';
  search?: string;
  tags?: string[];
  dateRange?: {
    start: string;
    end: string;
  };
}

export interface User {
  userId: string;
  email: string;
  name?: string;
  avatarUrl?: string;
}

// Extended meeting detail from API
export interface MeetingDetail extends Meeting {
  content?: string;
  transcriptA?: string;
  transcriptB?: string;
  selectedTranscript?: 'A' | 'B' | null;
  audioKey?: string;
  shares?: SharedUser[];
  isShared?: boolean;
  sharedBy?: string | null;
  permission?: 'read' | 'edit' | null;
}

export interface KBFile {
  fileId: string;
  fileName: string;
  fileType: string;
  size?: number;
  uploadedAt: string;
}

export interface QAEntry {
  id: string;
  question: string;
  answer: string;
  sources?: { title: string; snippet: string }[];
  timestamp: string;
}

export interface IntegrationConfig {
  configured: boolean;
  maskedKey?: string;
}

export interface IntegrationsResponse {
  notion?: IntegrationConfig;
}

export interface ExportResponse {
  url?: string;
  notionPageId?: string;
  notionUrl?: string;
  filename?: string;
  content?: string;
}

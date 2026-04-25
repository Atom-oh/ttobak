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
  sttProvider?: 'transcribe' | 'nova-sonic';
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
  type: 'image' | 'document' | 'audio' | 'video' | 'photo' | 'screenshot' | 'diagram' | 'whiteboard' | 'audio_file';
  url: string;
  thumbnailUrl?: string;
  originalUrl?: string;
  processedUrl?: string;
  size?: number;
  mimeType?: string;
  status?: string;
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
  notes?: string;
  transcriptA?: string;
  transcriptB?: string;
  selectedTranscript?: 'A' | 'B' | null;
  audioKey?: string;
  speakerMap?: Record<string, string>;
  shares?: SharedUser[];
  isShared?: boolean;
  sharedBy?: string | null;
  permission?: 'read' | 'edit' | null;
}

export interface KBFile {
  fileId: string;
  fileName: string;
  fileType?: string;
  size?: number;
  lastModified: string;
}

export interface QAEntry {
  id: string;
  question: string;
  answer: string;
  sources?: string[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
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

export interface CrawlerSource {
  sourceId: string;
  sourceName: string;
  subscribers: string[];
  awsServices: string[];
  newsQueries: string[];
  newsSources: string[];
  customUrls: string[];
  schedule: string;
  lastCrawledAt: string;
  status: string;
  documentCount: number;
}

export interface CrawlerSubscription {
  sourceId: string;
  awsServices: string[];
  newsSources: string[];
  customUrls: string[];
  addedAt: string;
}

export interface CrawlerSourceResponse {
  source: CrawlerSource;
  subscription: CrawlerSubscription;
}

export interface CrawledDocument {
  docHash: string;
  sourceId?: string;
  type: 'news' | 'tech';
  title: string;
  url: string;
  source?: string;
  summary?: string;
  awsServices?: string[];
  tags?: string[];
  s3Key?: string;
  crawledAt: number | string;
  inKB?: boolean;
  pubDate?: string;
}

export interface CrawlHistory {
  timestamp: string;
  docsAdded: number;
  docsUpdated: number;
  errors: string[];
  duration: number;
}

export interface ChatMessage {
  msgId: string;
  role: 'user' | 'agent';
  content: string;
  action?: 'propose_structure' | 'ask_question' | 'approve' | 'request_subpage' | 'respond';
  metadata?: string;
  createdAt: string;
}

export interface Research {
  researchId: string;
  userId?: string;
  topic: string;
  mode: 'quick' | 'standard' | 'deep';
  status: 'planning' | 'approved' | 'running' | 'done' | 'error';
  parentId?: string;
  createdAt: string;
  completedAt?: string;
  s3Key?: string;
  sourceCount?: number;
  wordCount?: number;
  summary?: string;
  errorMessage?: string;
}

export interface ResearchDetail extends Research {
  content?: string;
}

export interface ChatSession {
  sessionId: string;
  title: string;
  createdAt: string;
  lastMessageAt: string;
  messageCount: number;
}

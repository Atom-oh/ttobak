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
  sentiment?: 'positive' | 'neutral' | 'negative';
  participants?: Participant[];
  summary?: string;
  actionItems?: ActionItem[];
  transcription?: TranscriptSegment[];
  attachments?: Attachment[];
  sharedWith?: SharedUser[];
  createdAt: string;
  updatedAt: string;
  sttProvider?: 'transcribe' | 'nova-sonic';
  /** Per ADR-014: ordered S3 keys for multi-file uploads. Falls back to audioKey for legacy single-file meetings. */
  audioKey?: string;
  audioKeys?: string[];
  /** Per ADR-014 Phase 6: ordered predecessor meeting IDs whose summaries are prepended to this meeting's prompt. */
  linkedMeetingIds?: string[];
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
  processedContent?: string;
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
  audioKeys?: string[];
  audioPartCount?: number;
  audioPartsReady?: number;
  speakerMap?: Record<string, string>;
  shares?: SharedUser[];
  isShared?: boolean;
  sharedBy?: string | null;
  permission?: 'read' | 'edit' | null;
  accountId?: string;
  sharedToAccount?: boolean;
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
  parentPageId?: string; // empty on a legacy record saved before the parent-page requirement — needs re-connect
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
  trashedAt?: string;
  isShared?: boolean;
  sharedBy?: string;
  accountIds?: string[];
}

export interface ResearchDetail extends Research {
  content?: string;
  shares?: SharedUser[];
}

export interface AccountResearchRef {
  researchId: string;
  topic: string;
  summary?: string;
  status: Research['status'];
  ownerUserId: string;
  createdAt: string;
}

export interface AccountSummary {
  accountId: string;
  name: string;
  role: string;
}

export interface AccountMember {
  userId: string;
  email?: string;
  role: string;
}

export interface Account {
  accountId: string;
  name: string;
  aliases?: string[];
  domains?: string[];
  industry?: string;
  ownerUserId: string;
  members: AccountMember[];
  createdAt: string;
}

export interface AccountMeetingRef {
  meetingId: string;
  ownerUserId: string;
  title: string;
  date: string;
}

export interface AccountInsight {
  type: string;
  text: string;
  sourceType: string;
  sourceId: string;
  occurredAt: string;
  tsMarker?: string;
  entities?: string[];
}

export interface ProjectMember {
  userId: string;
  email?: string;
}

export interface ProjectSummary {
  projectId: string;
  name: string;
  stage?: string;
  sfdcOpptyId?: string;
}

export interface Project {
  projectId: string;
  name: string;
  description?: string;
  sfdcOpptyId?: string;
  sfdcUrl?: string;
  stage?: string;
  ownerUserId: string;
  accountIds: string[];
  members: ProjectMember[];
  createdAt: string;
  updatedAt: string;
}

export interface ProjectMeetingRef {
  meetingId: string;
  ownerUserId: string;
  title: string;
  date: string;
}

export interface ProjectResearchRef {
  researchId: string;
  topic: string;
  summary?: string;
  status: string;
  ownerUserId: string;
  createdAt: string;
}

export interface ProjectInsight {
  type: string;
  text: string;
  sourceId: string;
  occurredAt: string;
  tsMarker?: string;
  entities?: string[];
}

export interface ProjectBrief {
  project: Project;
  insightsByType: Record<string, ProjectInsight[]>;
  meetings: ProjectMeetingRef[];
  research: ProjectResearchRef[];
}

export interface AccountDocument {
  docId: string;
  title: string;
  docType?: string;
  path?: string;
  links?: string[];
  fileName?: string;
  sourceUserId: string;
  createdAt: string;
  updatedAt?: string;
  content?: string;
  downloadUrl?: string;
  /** Presigned URL for the PDF sidecar of a PPTX/PPT slide, once the
   * convert-doc Lambda's conversion has finished. Absent otherwise. */
  previewUrl?: string;
  /** Set once a public share link has been minted for this (personal,
   * slide) document; see docApi.createPublicShare. */
  publicShareToken?: string;
}

export interface PutDocumentRequest {
  title: string;
  docType?: string;
  path?: string;
  markdown?: string;
  fileKey?: string;
  fileName?: string;
  mimeType?: string;
  fileSize?: number;
}

export const INSIGHT_TYPES = [
  'trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action',
] as const;

export interface DictionaryTerm {
  phrase: string;
  soundsLike: string;
  displayAs: string;
}

export interface ChatSession {
  sessionId: string;
  title: string;
  createdAt: string;
  lastMessageAt: string;
  messageCount: number;
}

export interface DictionaryTerm {
  phrase: string;
  soundsLike: string;
  displayAs: string;
}

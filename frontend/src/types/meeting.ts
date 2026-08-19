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
  sttProvider?: 'transcribe' | 'nova-sonic' | 'whisper';
  /** Per ADR-014: ordered S3 keys for multi-file uploads. Falls back to audioKey for legacy single-file meetings. */
  audioKey?: string;
  audioKeys?: string[];
  /** Per ADR-014 Phase 6: ordered predecessor meeting IDs whose summaries are prepended to this meeting's prompt. */
  linkedMeetingIds?: string[];
  /** ADR-031 cost/sizing simulator, singleton per meeting. */
  simRun?: SimRun;
}

/** Mirrors backend/internal/model.SimRequirement's JSON shape exactly. */
export interface SimRequirement {
  key: string;
  label: string;
  value: string;
  unit?: string;
  required: boolean;
  source: 'extracted' | 'user';
  /** transcript://{segmentId} deep link (ADR-013), empty when user-entered. */
  evidence?: string;
}

export interface SimOption {
  name: string;
  description?: string;
}

export interface SimChart {
  key: string;
  url?: string;
}

/** Mirrors backend/internal/model.SimRunResponse's JSON shape (ADR-031). */
export interface SimRun {
  simRunId: string;
  status: 'extracted' | 'queued' | 'running' | 'done' | 'error';
  requirements?: SimRequirement[];
  options?: SimOption[];
  charts?: SimChart[];
  reportMarkdown?: string;
  codeKey?: string;
  priceSnapshotAt?: string;
  errorMessage?: string;
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
  /** Real-time summary built during recording (markdown incl. mermaid) */
  liveSummary?: string;
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
  /** User-editable display label, defaults to `topic` at creation. `topic` (the original prompt) stays immutable. */
  title?: string;
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

// Assignable account team roles (owner is server-assigned, never offered here).
// Keep in sync with backend/internal/model/account.go's AssignableRoles.
export const ASSIGNABLE_ACCOUNT_ROLES = ['AM', 'TAM', 'SSA', 'SA', 'SA Manager', 'AM Manager'] as const;

export interface AccountSummary {
  accountId: string;
  name: string;
  role: string;
}

export interface AccountMember {
  // Omitted (not empty string) on a pending grant -- the backend DTO uses
  // `json:"userId,omitempty"`.
  userId?: string;
  email?: string;
  role: string;
  // true when the invitee has an invited-but-not-yet-logged-in Cognito
  // account and no DynamoDB profile exists yet -- the grant is queued and
  // becomes a real membership automatically on their first login. userId
  // is omitted in that case.
  pending?: boolean;
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

export interface FieldInsight {
  type: string;
  text: string;
  evidence?: string;
  implication?: string;
  nextAction?: string;
  sourceType?: string;
  sourceId: string;
  occurredAt: string;
  tsMarker?: string;
  entities?: string[];
}

export interface AccountInsight extends FieldInsight {
  sourceType: string;
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

export type ProjectInsight = FieldInsight;

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
  /** Owner's email — present only on a document someone shared directly with
   * the current user. Read-only: edit/delete/share stay with the owner. */
  sharedBy?: string;
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

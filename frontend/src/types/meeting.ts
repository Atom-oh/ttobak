export type MeetingCategory = 'internal_team' | 'internal_1on1' | 'internal_training' | 'internal_other';

export interface Meeting {
  PK: string;
  SK: string;
  meetingId: string;
  userId: string;
  accountId?: string;
  /** Owner-set internal category; never present together with accountId. */
  meetingCategory?: MeetingCategory;
  projectIds?: string[];
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
  /** Complete mixed/untimed segments; absent for legacy or wholly timed results. */
  transcriptionV2?: TranscriptSegment[];
  attachments?: Attachment[];
  sharedWith?: SharedUser[];
  createdAt: string;
  updatedAt: string;
  sttProvider?: 'transcribe' | 'nova-sonic' | 'whisper';
  /** Per ADR-014: ordered S3 keys for multi-file uploads. Falls back to audioKey for legacy single-file meetings. */
  audioKey?: string;
  audioKeys?: string[];
  /** Ordered predecessor meeting IDs (ADR-014). Navigation only: their summaries are never generation evidence. */
  linkedMeetingIds?: string[];
  /** ADR-010 cost/sizing simulator, singleton per meeting. */
  simRun?: SimRun;
  /** List items: true for meetings shared with the viewer. */
  isShared?: boolean;
  /** List items: creator of a shared meeting (omitted for own meetings). */
  ownerUserId?: string;
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

/** Mirrors backend/internal/model.SimRunResponse's JSON shape (ADR-010). */
export interface SimRun {
  simRunId: string;
  status: 'extracted' | 'queued' | 'running' | 'done' | 'error';
  requirements?: SimRequirement[];
  options?: SimOption[];
  charts?: SimChart[];
  reportMarkdown?: string;
  codeKey?: string;
  /** Presigned download URL for codeKey (generated.py) -- codeKey alone is a raw S3 key, unreachable from the browser post-ADR-027. */
  codeUrl?: string;
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

export interface ActionItemsAnalysis {
  status: 'unknown' | 'queued' | 'running' | 'failed' | 'succeeded';
  runId?: string;
  errorCode?: string;
  leaseUntil?: number;
}

export interface ActionItemsResponse {
  actionItems: ActionItem[];
  analysis: ActionItemsAnalysis;
}

interface TranscriptSegmentBase {
  id: string;
  speaker: string;
  speakerInitials?: string;
  timestamp: string;
  text: string;
}

export type TranscriptSegment = TranscriptSegmentBase & (
  | { untimed?: false; startTime: number; endTime: number }
  | { untimed: true; startTime?: never; endTime?: never }
);

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
  originalKey?: string;
  textExtraction?: AttachmentTextStatus;
}

export interface AttachmentTextStatus {
  status: 'unknown' | 'queued' | 'running' | 'succeeded' | 'partial' | 'failed';
  runId?: string;
  errorCode?: string;
  leaseUntil?: number;
  updatedAt?: string;
  unitCount: number;
  complete: boolean;
  hasResult: boolean;
  needsResummary: boolean;
  summaryExcerpted: boolean;
}

export type IndexState = 'UNTRACKED' | 'PENDING' | 'PREPARING' | 'WAITING_SYNC' | 'WAITING_SOURCE' | 'INDEXED' | 'FAILED' | 'DELETED';
export interface IndexStatusResponse {
  state: IndexState;
  errorCode?: string;
  updatedAt?: string;
}
export interface ResummaryStatus {
  status: 'unknown' | 'queued' | 'running' | 'succeeded' | 'failed';
  runId?: string;
  errorCode?: string;
  leaseUntil?: number;
  updatedAt?: string;
  resultHash?: string;
  sharingSafe?: boolean;
}
/** GET /api/meetings/{id}/notes/en: status fields of the re-summary response plus the English note. */
export interface EnglishNote extends Omit<ResummaryStatus, 'sharingSafe'> {
  content?: string;
  generatedAt?: string;
  stale: boolean;
}
export interface SummaryReadingPage {
  meetingId: string;
  source: 'summary';
  revision: string;
  content: string;
  updatedAt?: string;
  page: {
    unit: 'unicode_code_points';
    startOffset: number;
    endOffset: number;
    totalCodePoints: number;
    complete: boolean;
    nextCursor?: string | null;
  };
}
export type IndexStatusTarget =
  | { kind: 'meeting'; meetingId: string }
  | { kind: 'document'; docId: string }
  | { kind: 'accountDocument'; accountId: string; docId: string };

export interface AttachmentTextLocation {
  kind: 'page' | 'slide' | 'paragraph';
  page?: number;
  slide?: number;
  paragraph?: number;
  startLine?: number;
  endLine?: number;
  table?: number;
  row?: number;
  cell?: number;
  part?: string;
  hidden?: boolean;
}

export interface AttachmentTextPage {
  analysis: AttachmentTextStatus;
  current: boolean;
  source: {
    bucket: string;
    key: string;
    eTag: string;
    meetingId: string;
    ownerId: string;
    uploaderId: string;
    attachmentId: string;
    runId: string;
  };
  format: 'pdf' | 'pptx' | 'docx' | 'md';
  scope: 'embedded_pdf_text' | 'slide_body' | 'document_body' | 'markdown_source';
  complete: boolean;
  warningCount: number;
  units: {
    unitIndex: number;
    startOffset: number;
    endOffset: number;
    text: string;
    location: AttachmentTextLocation;
  }[];
  nextCursor?: string;
  pageComplete: boolean;
}

export interface SharedUser {
  userId: string;
  email: string;
  name?: string;
  permission: 'read' | 'edit';
  sharedAt: string;
  /** Account member who shared on the creator's behalf. */
  sharedByEmail?: string;
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
  accountId?: string;
  accountIds?: string[];
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
export interface TranscriptQuality {
  refinement: 'validated' | 'partial' | 'raw' | 'mixed' | 'not_requested' | 'unknown' | 'edited';
  degraded: boolean;
  speakerProvenance: 'acoustic' | 'text_inferred' | 'mixed' | 'unknown';
  totalChunks?: number;
  fallbackChunks?: number;
  timing?: 'available' | 'partial' | 'unavailable';
  blankSegments?: number;
  untimedSegments?: number;
  reasons?: string[];
  diarizationEnabled?: boolean;
  detectedSpeakers?: number;
}

export interface MeetingDetail extends Meeting {
  summarySharing?: {
    canCreateGrant: boolean;
    canShareToAccount?: boolean;
    derivedReadRestricted: boolean;
    requiresRegeneration: boolean;
    canRegenerate: boolean;
  };
  transcriptQuality?: TranscriptQuality;
  canRecoverRecording?: boolean;
  /** Creator or live account-team member: every creator action except delete. */
  canManage?: boolean;
  canCorrectSpeakers?: boolean;
  supportsAudioCrop?: boolean;
  /** Server flag for POST /api/meetings/{id}/translation; absent means off. */
  supportsMeetingTranslation?: boolean;
  audioCrop?: { sourceMeetingId: string; startSeconds: number; endSeconds: number; state: string };
  supportsNotesComparison?: boolean;
  notesRevision?: string;
  supportsPrivateAccountLink?: boolean;
  fieldInsights?: MeetingFieldInsight[];
  fieldInsightsError?: string;
  fieldInsightsTruncated?: boolean;
  actionItemsAnalysis?: ActionItemsAnalysis;
  content?: string;
  notes?: string;
  /** Real-time summary built during recording (markdown incl. mermaid) */
  liveSummary?: string;
  transcriptA?: string;
  transcriptB?: string;
  selectedTranscript?: 'A' | 'B' | null;
  /** Generated-note language: '' automatic, 'ko' Korean, 'en' English. */
  noteLanguage?: NoteLanguage;
  /** An English note exists; its text is fetched separately. */
  hasEnglishNote?: boolean;
  audioKey?: string;
  audioKeys?: string[];
  audioPartCount?: number;
  audioPartsReady?: number;
  speakerMap?: Record<string, string>;
  shares?: SharedUser[];
  isShared?: boolean;
  sharedBy?: string | null;
  permission?: 'read' | 'edit' | null;
  sharedToAccount?: boolean;
}

/** Stored extraction, without a current-summary freshness guarantee. */
export interface MeetingFieldInsight {
  id: string;
  type: string;
  text: string;
  evidence?: string;
  implication?: string;
  nextAction?: string;
  tsMarker?: string;
  entities?: string[];
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
  sourceDetails?: QASourceDetail[];
  usedKB?: boolean;
  usedDocs?: boolean;
  toolsUsed?: string[];
  timestamp: string;
}

/** Canonical retrieval and attachment provenance; legacyText has no PK/SK. */
export interface QASourceDetail {
  resourceKind: string;
  resourceId?: string;
  sourcePK?: string;
  sourceSK?: string;
  title?: string;
  sourceRevision?: string;
  uri?: string;
  contentSource?: string;
  provenanceScope?: 'validated_history' | 'legacy_identity' | 'history_receipt' | 'legacy_identity_unavailable';
  meetingId?: string;
  attachmentId?: string;
  filePending?: boolean;
  usingPreviousResult?: boolean;
  partial?: boolean;
  locations?: AttachmentTextLocation[];
  attempt?: { status?: string; runId?: string; errorCode?: string; [key: string]: unknown };
  result?: { runId?: string; complete?: boolean; [key: string]: unknown };
  [key: string]: unknown;
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
  ownerId?: string;
  sourceName: string;
  subscribers: string[];
  awsServices: string[];
  newsQueries: string[];
  newsSources: string[];
  customUrls: string[];
  /** Public alternate names used only to widen news search coverage. */
  aliases?: string[];
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
  // Optional publication diagnostics; absent from legacy/tech history.
  runId?: string;
  status?: string;
  queryCount?: number;
  invalidRejected?: number;
  staleRejected?: number;
  duplicateRejected?: number;
  budgetRejected?: number;
  attemptCount?: number;
  expiredAttempts?: number;
  unknownAttempts?: number;
  chargedUsd?: string;
  usageComplete?: boolean;
  workerOutcomes?: Record<string, number>;
  fetchWarnings?: string[];
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
  parentAccountId?: string;
  accessPermission?: 'read' | 'edit';
  inheritedFromAccountId?: string;
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
  // The current member-add request asked Cognito to send an invitation.
  invited?: boolean;
}

export interface ContactInput {
  name: string;
  company?: string;
  department?: string;
  title?: string;
  email?: string;
  address?: string;
  workPhone?: string;
  fax?: string;
  phone?: string;
  cardRegisteredAt?: string;
  cardBook?: string;
  group?: string;
  notes?: string;
}

export interface AccountContact extends ContactInput { contactId: string; revision: string }

/** A shared event (schedule) of an account; direct members only (ADR-021, contact encryption). */
export interface EventAttendee { contactId?: string; name: string; email?: string }
export interface AccountEventInput {
  revision?: string;
  title: string;
  startAt: string;
  endAt?: string;
  allDay?: boolean;
  location?: string;
  attendees?: EventAttendee[];
  notes?: string;
}
export interface AccountEvent extends AccountEventInput {
  eventId: string;
  revision: string;
  attendees: EventAttendee[];
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}
export interface AccountSpeakerTeamMember { userId: string; name: string; email: string; role: string }

export interface Account {
  accountId: string;
  name: string;
  parentAccountId?: string;
  accessPermission?: 'read' | 'edit';
  inheritedFromAccountId?: string;
  aliases?: string[];
  domains?: string[];
  industry?: string;
  ownerUserId: string;
  // Initial invitation hint; resend verifies the current Cognito status.
  ownerInvitationPending?: boolean;
  /** Set while this Account merges into another; writes are fenced. */
  mergingInto?: string;
  /** Set on the tombstone left after a merge finished. */
  mergedInto?: string;
  members: AccountMember[];
  createdAt: string;
}

export interface AccountMergePreview {
  fromAccountId: string;
  intoAccountId: string;
  /** Per-category counts; -1 means the bounded read cap was exceeded. */
  counts: Record<string, number>;
  unmoved: { category: string; present: boolean; unverifiable?: boolean }[];
  cliOnly: string[];
  newAudience: { intoMembersNotInFrom: number; intoHasAncestors: boolean };
  requiresAcceptUnmoved: boolean;
}

export interface AccountMergeJob {
  fromAccountId: string;
  intoAccountId: string;
  state: 'running' | 'failed' | 'done' | 'aborted';
  step: string;
  errorCode?: string;
  counts: Record<string, number>;
  startedAt: string;
  updatedAt: string;
  finishedAt?: string;
  abortedAt?: string;
}

export interface AccountCreationRequest {
  name: string;
  aliases?: string[];
  domains?: string[];
  industry?: string;
  parentAccountId?: string;
  ownerEmail?: string;
  keepCreatorAsMember?: boolean;
  requestId?: string;
}

export interface AccountCreationResponse extends Account {
  creatorMember: boolean;
  // Initial invitation state retained in this creation receipt.
  ownerInvitationPending: boolean;
  ownerInvitationRequested: boolean;
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
  stageChangedAt?: string;
  stageMapped?: boolean;
  sfdcOpptyId?: string;
}

export interface Project {
  projectId: string;
  name: string;
  description?: string;
  sfdcOpptyId?: string;
  sfdcUrl?: string;
  stage?: string;
  stageChangedAt?: string;
  stageMapped?: boolean;
  ownerUserId: string;
  accessPermission?: 'read' | 'edit';
  inheritedFromAccountId?: string;
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
  accessPermission?: 'read' | 'edit';
  inheritedFromAccountId?: string;
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

export interface ChatSession {
  sessionId: string;
  title: string;
  createdAt: string;
  lastMessageAt: string;
  messageCount: number;
}

/** One translated reading page; held in client memory only, never stored. */
export interface MeetingTranslationPage {
  meetingId: string;
  part: 'notes' | 'transcript';
  target: 'ko' | 'en';
  revision: string;
  mode?: 'segments' | 'text';
  notes?: string;
  segments?: { segmentId?: string; speaker?: string; startTime?: number; endTime?: number; partial?: boolean; text: string }[];
  engines: Record<string, number>;
  untranslatedLines: number;
  nextCursor: string | null;
  complete: boolean;
  persisted: false;
}

/** Allowlisted values of the meeting noteLanguage field. */
export type NoteLanguage = '' | 'ko' | 'en';

// ---- Account overview: links, signals, flow, graph (API-SPEC account routes) ----

export type AccountLinkKind = 'news' | 'tech' | 'event' | 'meeting' | 'project';

/** News/tech IDs are {sourceId}:{docHash}; meeting IDs {ownerUserId}:{meetingId}. */
export interface AccountLinkInput {
  fromKind: AccountLinkKind;
  fromId: string;
  toKind: AccountLinkKind;
  toId: string;
}

export interface AccountLink extends AccountLinkInput {
  origin: 'manual' | 'auto' | string;
  createdBy?: string;
  createdAt: string;
  evidence?: string;
}

export interface StallReason { code: string; detail: string }

export interface SignalNews {
  id: string;
  title: string;
  url?: string;
  pubDate?: string;
  awsRelevance?: string;
  qualityScore: number;
  linkedProjectIds: string[];
  candidate: boolean;
}

export interface SignalOppty {
  projectId: string;
  name: string;
  stage: string;
  stageMapped: boolean;
  daysInStage?: number | null;
  terminal: boolean;
  stall: StallReason[];
}

export interface AccountSignals {
  newsState: 'OK' | 'NO_SOURCE' | 'NO_RECENT_NEWS' | string;
  news: SignalNews[];
  opportunities: SignalOppty[];
  insights: AccountInsight[];
  restrictedCount: number;
  linksTruncated: boolean;
}

export interface FlowItem {
  kind: 'event' | 'meeting';
  id: string;
  title: string;
  date?: string;
  origin?: string;
  evidence?: string;
  need?: string;
  /** At most 5 readable people of the item (attendee, participant, speaker, assignee). */
  people?: FlowPerson[];
}

export interface FlowPerson { kind: 'member' | 'contact'; id: string; label: string; role: string }

export interface ProjectStageChange {
  projectId: string;
  from: string;
  to: string;
  changedBy: string;
  changedAt: string;
}

export interface FlowRow {
  projectId?: string;
  activity: FlowItem[];
  response: FlowItem[];
  oppty?: SignalOppty;
  history: ProjectStageChange[];
  result: { state: string; stall: StallReason[] };
}

export interface AccountFlow {
  rows: FlowRow[];
  unassigned: FlowRow;
  truncated: boolean;
}

export interface AccountGraphNode { id: string; kind: string; label: string }
/** `role` is set only on `person` edges. */
export interface AccountGraphEdge { from: string; to: string; kind: string; origin?: string; role?: string }
/** `memberCount` counts direct members; only members with a people edge are nodes. */
export interface AccountGraph { nodes: AccountGraphNode[]; edges: AccountGraphEdge[]; truncated: boolean; memberCount?: number }

export type PeopleRole = 'participant' | 'speaker' | 'assignee' | 'mentions';

/** Focus graph (GET /api/accounts/{id}/people/focus). Meeting ids are `{ownerUserId}:{meetingId}`. */
export type FocusKind = 'contact' | 'member' | 'project';
export interface FocusRef { kind: FocusKind; id: string }
export interface FocusCard { kind: FocusKind; id: string; label: string; detail?: string; meetings: number }
export interface FocusPerson { kind: 'member' | 'contact'; id: string; label: string; detail?: string; roles: PeopleRole[] }
export interface FocusMeeting { id: string; title: string; date: string; roles: PeopleRole[]; techs: string[]; people: FocusPerson[] }
export interface FocusTech { id: string; label: string; group: string; meetings: number }
export interface FocusTag { tag: string; meetings: number }
export interface AccountFocusGraph {
  focus: FocusCard;
  meetings: FocusMeeting[];
  techs: FocusTech[];
  other: FocusTag[];
  nextCursor?: string;
  truncated: boolean;
}

/** One resolved member or contact of the account people panel. */
export interface AccountPerson { kind: 'member' | 'contact'; id: string; label: string; meetings: number; roles: Record<string, number> }
export interface AccountPeoplePage { people: AccountPerson[]; nextCursor?: string; truncated: boolean }

/** One normalized unresolved mention across meetings; `key` is its 32-hex hash. */
export interface MentionGroup { key: string; text: string; meetings: number; roles: string[] }
export interface MentionGroupsPage { groups: MentionGroup[]; nextCursor?: string; truncated: boolean }

/** A people edge. `meetingId` is the link form `{ownerUserId}:{meetingId}`. */
export interface PeopleLink {
  meetingId: string;
  toKind: string;
  toId: string;
  role: string;
  origin: 'auto' | 'manual' | string;
  rule?: string;
  sourceField: string;
  mention?: string;
  sourceRef?: string;
  evidence?: string;
  createdBy?: string;
  createdAt: string;
}

/** People edges of one meeting; `labels` is keyed `{kind}:{id}` and holds only readable targets. */
export interface MeetingPeopleEdges { edges: PeopleLink[]; labels: Record<string, string> }
export interface PeopleEdgeKey { meetingId: string; toKind: string; toId: string; role: string }
export interface PeopleEdgeInput extends PeopleEdgeKey { sourceRef?: string }
export interface ResolveMentionResult { written: number; remaining: boolean }

export interface CreateAccountNewsSourceInput {
  sourceName: string;
  newsQueries?: string[];
  aliases?: string[];
  copyFromSourceId?: string;
}

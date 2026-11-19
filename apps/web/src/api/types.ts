// API types mirroring docs/API_SPEC.md (v1). Kept local to apps/web on purpose:
// packages/shared is written in parallel and this app must not depend on it.
// Fields the spec does not pin down are marked with a comment and listed in the
// web README/report as assumptions.

export type Role = 'reporter' | 'developer' | 'maintainer' | 'admin';

export interface User {
  id: string;
  username: string;
  role: Role;
  createdAt: string;
  disabled?: boolean;
}

export interface Meta {
  version: string;
  needsSetup: boolean;
}

export interface Health {
  status: string;
  version: string;
  schemaVersion: number;
  draining: boolean;
  uptimeSec: number;
}

export interface ApiErrorBody {
  error: { code: ApiErrorCode; message: string };
}

export type ApiErrorCode =
  | 'not_found'
  | 'bad_request'
  | 'unauthorized'
  | 'forbidden'
  | 'conflict'
  | 'illegal_transition'
  | 'tls_required'
  | 'rate_limited'
  | 'internal';

export interface ApiToken {
  id: string;
  name: string; // spec: POST /tokens {name}; list shape assumed to carry these fields
  createdAt: string;
  lastUsedAt?: string | null;
  revokedAt?: string | null;
}

export interface ProjectStats {
  openRequests: number;
  activeTasks: number;
  reviewReady: number;
  failed: number;
}

export interface GitInfo {
  branch: string;
  head: string;
  dirty: boolean;
  ahead?: number;
}

export type ReleaseStrategy = 'none' | 'tag' | 'command' | 'rhymehub-self';
export type AgentName = 'claude' | 'codex' | string;

export interface Project {
  id: string;
  name: string;
  repoPath: string;
  gitUrl: string;
  defaultBranch: string;
  type: string;
  stack: string;
  buildCmd: string;
  testCmd: string;
  releaseStrategy: ReleaseStrategy;
  releaseCmd: string;
  defaultAgent: AgentName;
  defaultModel: string;
  maxConcurrent: number;
  timeoutMinutes: number;
  autoTaskFromRequests: boolean;
  autoApprove: boolean;
  autoRelease: boolean;
  allowedTools: string[];
  sdkStatus: 'none' | 'configured' | 'active' | string;
  installedVersion: string;
  latestVersion: string;
  isSelf: boolean;
  archived: boolean;
  createdAt: string;
  updatedAt: string;
  stats: ProjectStats;
  git?: GitInfo;
}

export interface ProjectCandidate {
  path: string;
  name: string;
  stack: string;
  registered: boolean;
}

export interface ProjectKey {
  id: string;
  name: string;
  keyPrefix: string;
  allowedOrigins: string[];
  createdAt: string;
  lastUsedAt?: string | null;
  revokedAt?: string | null;
}

export interface CreatedProjectKey {
  id: string;
  key: string;
  keyPrefix: string;
}

export interface GitCommit {
  hash: string;
  subject: string;
  author: string;
  at: string;
}

export interface ProjectGit extends GitInfo {
  recentCommits: GitCommit[];
}

export type RequestType =
  | 'bug'
  | 'feature'
  | 'improvement'
  | 'refactor'
  | 'performance'
  | 'security'
  | 'docs'
  | 'release';

export type RequestSource = 'dashboard' | 'sdk' | 'cli' | 'api';

export type RequestStatus =
  | 'new'
  | 'triaged'
  | 'in_progress'
  | 'waiting_input'
  | 'review'
  | 'done'
  | 'rejected'
  | 'failed';

export interface Diagnostic {
  kind: 'log' | 'error' | 'context' | string;
  name: string;
  content?: string;
  attachmentId?: string;
}

export interface Request {
  id: string;
  projectId: string;
  source: RequestSource;
  type: RequestType;
  title: string;
  description: string;
  priority: number; // 0 low, 1 normal-, 2 normal, 3 high, 4 urgent
  status: RequestStatus;
  installationId?: string | null;
  appVersion?: string | null;
  platform?: string | null;
  diagnostics: Diagnostic[];
  redactions: number;
  taskId?: string | null;
  task?: TaskSummary | null;
  createdAt: string;
  updatedAt: string;
}

export type TaskState =
  | 'CREATED'
  | 'QUEUED'
  | 'PREPARING'
  | 'RUNNING'
  | 'WAITING_INPUT'
  | 'WAITING_APPROVAL'
  | 'VALIDATING'
  | 'REVIEW_READY'
  | 'APPROVED'
  | 'MERGING'
  | 'RELEASING'
  | 'COMPLETED'
  | 'FAILED'
  | 'CANCELLED'
  | 'RETRYING'
  | 'ROLLED_BACK';

export type TestStatus = '' | 'passed' | 'failed' | 'skipped';

export interface ToolDenial {
  tool: string;
  input: unknown;
}

export interface TaskSummary {
  id: string;
  projectId: string;
  requestId: string | null;
  title: string;
  agent: AgentName;
  model: string;
  state: TaskState;
  priority: number;
  branch: string;
  testStatus: TestStatus;
  diffStat: string;
  resultSummary: string;
  pendingQuestion: string | null;
  pendingApproval: ToolDenial[] | null;
  failureReason: string;
  error: string;
  costUsd: number;
  turns: number;
  attempt: number;
  createdAt: string;
  updatedAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface Task extends TaskSummary {
  prompt: string;
  worktreePath: string;
  baseCommit: string;
  headCommit: string;
  sessionId: string;
  testOutput: string;
  allowedTools: string[];
  approvedBy: string | null;
  releaseId: string | null;
}

export type TaskEventType =
  | 'state_changed'
  | 'user_message'
  | 'agent_text'
  | 'tool_use'
  | 'tool_result'
  | 'agent_error'
  | 'usage'
  | 'question'
  | 'answer'
  | 'permission_request'
  | 'permission_decision'
  | 'test_result'
  | 'diff'
  | 'merge'
  | 'release'
  | 'log';

export interface TaskEvent {
  id: number;
  taskId: string;
  seq: number;
  type: TaskEventType | string;
  payload: Record<string, unknown>;
  createdAt: string;
  projectId?: string;
}

export interface DiffFileSummary {
  path: string;
  status: string;
  additions: number;
  deletions: number;
}

export interface TaskDiff {
  base: string;
  head: string;
  stat: string;
  files: DiffFileSummary[];
  patch: string;
}

export type ReleaseStatus = 'building' | 'published' | 'failed' | 'rolled_back' | string;

export interface ReleaseArtifact {
  name: string;
  sha256: string;
  size: number;
  signed: boolean;
}

export interface Release {
  id: string;
  projectId: string;
  version: string;
  tag: string;
  strategy: ReleaseStrategy | string;
  taskIds: string[];
  notes: string;
  status: ReleaseStatus;
  artifacts: ReleaseArtifact[];
  log: string;
  createdBy: string;
  approvedBy: string | null;
  createdAt: string;
}

export interface AgentCapabilities {
  streaming: boolean;
  resume: boolean;
  multiTurn: boolean;
  permissionRequests: boolean;
  interactiveApproval: boolean;
  cancel: boolean;
  costReporting: boolean;
  models: string[];
}

export interface AgentInfo {
  name: string;
  available: boolean;
  version: string;
  path: string;
  capabilities: AgentCapabilities;
  running: number;
  limit: number;
  error?: string;
}

export interface AgentLimits {
  global: number;
  perAgent: Record<string, number>;
}

export interface SupervisorStatus {
  reachable: boolean;
  version: string;
  currentVersion: string;
  updateInProgress: boolean;
}

export interface SystemStatus {
  version: string;
  schemaVersion: number;
  uptimeSec: number;
  draining: boolean;
  home: string;
  activeWorkers: number;
  supervisor: SupervisorStatus;
}

export type UpdateStatus = 'succeeded' | 'rolled_back' | 'failed';

export interface UpdateRecord {
  id: string;
  from: string;
  to: string;
  status: UpdateStatus;
  phase: string;
  error: string;
  startedAt: string;
  finishedAt: string | null;
  dbBackup: string;
}

export interface AvailableUpdate {
  version: string;
  publishedAt: string;
  notes: string;
}

export interface SystemUpdates {
  available: AvailableUpdate[];
  history: UpdateRecord[];
}

export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  actorRole: string;
  action: string;
  targetType: string;
  targetId: string;
  detail: string;
  ip: string;
}

export type NotificationKind = string;

export interface AppNotification {
  id: string;
  kind: NotificationKind;
  title: string;
  body: string;
  url: string;
  createdAt: string;
  readAt: string | null;
}

export interface PushSubscribeBody {
  endpoint: string;
  keys: { p256dh: string; auth: string };
}

// Event bodies delivered over GET /api/v1/events/stream.
export type StreamTaskEvent = TaskEvent & { projectId: string };
export interface StreamSystemEvent {
  draining: boolean;
  version: string;
}
export interface StreamNotificationEvent {
  id: string;
  kind: NotificationKind;
  title: string;
  body: string;
  url: string;
}

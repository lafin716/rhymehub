import { apiRequest, unwrapList } from './client';
import type {
  AgentInfo,
  AgentLimits,
  AppNotification,
  AuditEntry,
  ApiToken,
  CreatedProjectKey,
  Health,
  Meta,
  Project,
  ProjectCandidate,
  ProjectGit,
  ProjectKey,
  PushSubscribeBody,
  Release,
  Request,
  RequestSource,
  RequestStatus,
  RequestType,
  SdkPrompt,
  Role,
  SystemStatus,
  SystemUpdates,
  Task,
  TaskDiff,
  TaskEvent,
  TaskSummary,
  User,
} from './types';

// Every endpoint is relative to /api/v1 except /healthz (see buildUrl in client.ts).

export const endpoints = {
  // Public
  health: () => apiRequest<Health>('/healthz'),
  meta: () => apiRequest<Meta>('/meta'),
  setup: (body: { username: string; password: string }) =>
    apiRequest<{ user?: User } | undefined>('/setup', { method: 'POST', body }),

  // Auth
  login: (body: { username: string; password: string }) =>
    apiRequest<{ user: User }>('/auth/login', { method: 'POST', body }),
  logout: () => apiRequest<void>('/auth/logout', { method: 'POST' }),
  me: () => apiRequest<{ user: User }>('/auth/me'),

  // Tokens (any authenticated user manages their own)
  listTokens: async (): Promise<ApiToken[]> =>
    unwrapList<ApiToken>(await apiRequest<unknown>('/tokens'), 'tokens'),
  createToken: (name: string) => apiRequest<{ token: string; id: string }>('/tokens', { method: 'POST', body: { name } }),
  revokeToken: (id: string) => apiRequest<void>(`/tokens/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  // Users (admin)
  listUsers: async (): Promise<User[]> => unwrapList<User>(await apiRequest<unknown>('/users'), 'users'),
  createUser: (body: { username: string; password: string; role: Role }) =>
    apiRequest<User>('/users', { method: 'POST', body }),
  updateUser: (id: string, body: { role?: Role; disabled?: boolean; password?: string }) =>
    apiRequest<User>(`/users/${encodeURIComponent(id)}`, { method: 'PATCH', body }),

  // Projects
  listProjects: async (q?: string): Promise<Project[]> =>
    unwrapList<Project>(await apiRequest<unknown>('/projects', { query: { q } }), 'projects'),
  getProject: (id: string) => apiRequest<Project>(`/projects/${encodeURIComponent(id)}`),
  createProject: (body: Partial<Project> & { name: string; repoPath?: string; gitUrl?: string }) =>
    apiRequest<Project>('/projects', { method: 'POST', body }),
  updateProject: (id: string, body: Partial<Project>) =>
    apiRequest<Project>(`/projects/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  discoverProjects: async (root: string): Promise<ProjectCandidate[]> =>
    unwrapList<ProjectCandidate>(await apiRequest<unknown>('/projects/discover', { method: 'POST', body: { root } }), 'candidates'),
  listProjectKeys: async (id: string): Promise<ProjectKey[]> =>
    unwrapList<ProjectKey>(await apiRequest<unknown>(`/projects/${encodeURIComponent(id)}/keys`), 'keys'),
  createProjectKey: (id: string, body: { name: string; allowedOrigins?: string[] }) =>
    apiRequest<CreatedProjectKey>(`/projects/${encodeURIComponent(id)}/keys`, { method: 'POST', body }),
  deleteProjectKey: (id: string, keyId: string) =>
    apiRequest<void>(`/projects/${encodeURIComponent(id)}/keys/${encodeURIComponent(keyId)}`, { method: 'DELETE' }),
  getSdkPrompt: (id: string, serverUrl?: string) =>
    apiRequest<SdkPrompt>(
      `/projects/${encodeURIComponent(id)}/sdk-prompt${serverUrl ? `?serverUrl=${encodeURIComponent(serverUrl)}` : ''}`,
    ),
  projectGit: (id: string) => apiRequest<ProjectGit>(`/projects/${encodeURIComponent(id)}/git`),
  createProjectRelease: (id: string, body: { version?: string; notes?: string }) =>
    apiRequest<Release>(`/projects/${encodeURIComponent(id)}/releases`, { method: 'POST', body }),

  // Requests (Inbox)
  listRequests: async (params: {
    projectId?: string;
    status?: RequestStatus | '';
    source?: RequestSource | '';
    type?: RequestType | '';
    limit?: number;
  } = {}): Promise<Request[]> =>
    unwrapList<Request>(await apiRequest<unknown>('/requests', { query: params }), 'requests'),
  getRequest: (id: string) => apiRequest<Request>(`/requests/${encodeURIComponent(id)}`),
  createRequest: (body: { projectId: string; type: RequestType; title?: string; description: string; priority?: number }) =>
    apiRequest<Request>('/requests', { method: 'POST', body }),
  updateRequest: (id: string, body: { priority?: number; status?: 'triaged' | 'rejected' }) =>
    apiRequest<Request>(`/requests/${encodeURIComponent(id)}`, { method: 'PATCH', body }),
  startRequestTask: (id: string, body: { agent?: string; model?: string } = {}) =>
    apiRequest<Task>(`/requests/${encodeURIComponent(id)}/task`, { method: 'POST', body }),

  // Tasks
  listTasks: async (params: { projectId?: string; state?: string; active?: boolean; limit?: number } = {}): Promise<TaskSummary[]> =>
    unwrapList<TaskSummary>(
      await apiRequest<unknown>('/tasks', { query: { ...params, active: params.active ? 1 : undefined } }),
      'tasks',
    ),
  getTask: (id: string) => apiRequest<Task>(`/tasks/${encodeURIComponent(id)}`),
  createTask: (body: { projectId: string; prompt: string; title?: string; agent?: string; model?: string; priority?: number }) =>
    apiRequest<Task>('/tasks', { method: 'POST', body }),
  listTaskEvents: async (id: string, after?: number, limit = 500): Promise<TaskEvent[]> =>
    unwrapList<TaskEvent>(
      await apiRequest<unknown>(`/tasks/${encodeURIComponent(id)}/events`, { query: { after, limit } }),
      'events',
    ),
  sendTaskMessage: (id: string, text: string) =>
    apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/messages`, { method: 'POST', body: { text } }),
  answerTask: (id: string, text: string) =>
    apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/answer`, { method: 'POST', body: { text } }),
  decidePermission: (id: string, allow: boolean) =>
    apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/permission`, { method: 'POST', body: { allow } }),
  cancelTask: (id: string) => apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
  retryTask: (id: string) => apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/retry`, { method: 'POST' }),
  taskDiff: (id: string) => apiRequest<TaskDiff>(`/tasks/${encodeURIComponent(id)}/diff`),
  approveTask: (id: string, release = false) =>
    apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/approve`, { method: 'POST', body: { release } }),
  rejectTask: (id: string, reason: string) =>
    apiRequest<Task>(`/tasks/${encodeURIComponent(id)}/reject`, { method: 'POST', body: { reason } }),

  // Agents
  listAgents: () => apiRequest<{ agents: AgentInfo[]; limits: AgentLimits }>('/agents'),
  updateAgentLimits: (body: { global?: number; perAgent?: Record<string, number> }) =>
    apiRequest<AgentLimits>('/agents/limits', { method: 'PATCH', body }),

  // Releases
  listReleases: async (projectId?: string, limit?: number): Promise<Release[]> =>
    unwrapList<Release>(await apiRequest<unknown>('/releases', { query: { projectId, limit } }), 'releases'),
  getRelease: (id: string) => apiRequest<Release>(`/releases/${encodeURIComponent(id)}`),

  // System
  systemStatus: () => apiRequest<SystemStatus>('/system/status'),
  systemUpdates: () => apiRequest<SystemUpdates>('/system/updates'),
  startUpdate: (version: string) =>
    apiRequest<{ accepted: boolean }>('/system/update', { method: 'POST', body: { version } }),
  // ASSUMPTION: API_SPEC does not list a rollback route. POST /system/rollback {updateId} is assumed.
  rollbackUpdate: (updateId: string) =>
    apiRequest<{ accepted: boolean }>('/system/rollback', { method: 'POST', body: { updateId } }),
  listAudit: async (limit = 100): Promise<AuditEntry[]> =>
    unwrapList<AuditEntry>(await apiRequest<unknown>('/audit', { query: { limit } }), 'entries'),

  // Push + notifications
  vapidPublicKey: () => apiRequest<{ key: string }>('/push/vapid-public-key'),
  subscribePush: (body: PushSubscribeBody) => apiRequest<void>('/push/subscribe', { method: 'POST', body }),
  unsubscribePush: (endpoint: string) => apiRequest<void>('/push/subscribe', { method: 'DELETE', body: { endpoint } }),
  testPush: () => apiRequest<void>('/push/test', { method: 'POST' }),
  listNotifications: async (limit = 100): Promise<AppNotification[]> => {
    const body = await apiRequest<unknown>('/notifications', { query: { limit } });
    return unwrapList<AppNotification>(body, 'notifications');
  },
  markNotificationsRead: (ids: string[]) =>
    apiRequest<void>('/notifications/read', { method: 'POST', body: { ids } }),
};

/** Pages `GET /tasks/{id}/events` until the backlog after `afterSeq` is drained. */
export async function fetchEventBacklog(taskId: string, afterSeq: number, pageSize = 500): Promise<TaskEvent[]> {
  const all: TaskEvent[] = [];
  let cursor = afterSeq;
  for (;;) {
    const page = await endpoints.listTaskEvents(taskId, cursor, pageSize);
    all.push(...page);
    if (page.length < pageSize) break;
    const last = page[page.length - 1];
    if (!last || last.seq <= cursor) break;
    cursor = last.seq;
  }
  return all;
}

export type { ApiToken, AppNotification, Request, Project, Task, TaskSummary };

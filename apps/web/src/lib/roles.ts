import type { Role } from '../api/types';

/** Ascending privilege order: reporter < developer < maintainer < admin. */
export const ROLE_ORDER: readonly Role[] = ['reporter', 'developer', 'maintainer', 'admin'];

export const ROLE_LABEL: Record<Role, string> = {
  reporter: 'Reporter',
  developer: 'Developer',
  maintainer: 'Maintainer',
  admin: 'Admin',
};

export function hasRole(role: Role | null | undefined, minimum: Role): boolean {
  if (!role) return false;
  return ROLE_ORDER.indexOf(role) >= ROLE_ORDER.indexOf(minimum);
}

/** Named UI capabilities. The server enforces the real rules; this only hides controls. */
export type Capability =
  | 'viewAll'
  | 'createProject'
  | 'createTask'
  | 'manageRequests'
  | 'taskFollowUp'
  | 'taskPermission'
  | 'taskCancelRetry'
  | 'approve'
  | 'projectPolicies'
  | 'projectKeys'
  | 'release'
  | 'selfPolicies'
  | 'manageUsers'
  | 'agentLimits'
  | 'systemUpdate'
  | 'viewAudit';

const MIN_ROLE: Record<Capability, Role> = {
  viewAll: 'reporter',
  createProject: 'developer',
  createTask: 'developer',
  manageRequests: 'developer',
  taskFollowUp: 'developer',
  taskPermission: 'developer',
  taskCancelRetry: 'developer',
  approve: 'maintainer',
  projectPolicies: 'maintainer',
  projectKeys: 'maintainer',
  release: 'maintainer',
  selfPolicies: 'admin',
  manageUsers: 'admin',
  agentLimits: 'admin',
  systemUpdate: 'admin',
  viewAudit: 'admin',
};

export function can(role: Role | null | undefined, capability: Capability): boolean {
  return hasRole(role, MIN_ROLE[capability]);
}

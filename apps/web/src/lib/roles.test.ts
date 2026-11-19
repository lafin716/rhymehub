import { describe, expect, it } from 'vitest';
import { can, hasRole, ROLE_ORDER } from './roles';

describe('roles', () => {
  it('orders reporter < developer < maintainer < admin', () => {
    expect([...ROLE_ORDER]).toEqual(['reporter', 'developer', 'maintainer', 'admin']);
  });

  it('hasRole compares the hierarchy and rejects missing roles', () => {
    expect(hasRole('admin', 'reporter')).toBe(true);
    expect(hasRole('developer', 'developer')).toBe(true);
    expect(hasRole('reporter', 'developer')).toBe(false);
    expect(hasRole(null, 'reporter')).toBe(false);
    expect(hasRole(undefined, 'reporter')).toBe(false);
  });

  it('gates task creation at developer and approval at maintainer', () => {
    expect(can('reporter', 'createTask')).toBe(false);
    expect(can('developer', 'createTask')).toBe(true);
    expect(can('developer', 'approve')).toBe(false);
    expect(can('maintainer', 'approve')).toBe(true);
  });

  it('keeps user, audit and system controls at admin only', () => {
    for (const cap of ['manageUsers', 'agentLimits', 'systemUpdate', 'viewAudit', 'selfPolicies'] as const) {
      expect(can('maintainer', cap)).toBe(false);
      expect(can('admin', cap)).toBe(true);
    }
  });

  it('lets every signed-in role view the project list', () => {
    expect(can('reporter', 'viewAll')).toBe(true);
    expect(can(null, 'viewAll')).toBe(false);
  });
});

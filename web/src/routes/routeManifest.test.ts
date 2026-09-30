import { describe, expect, it } from 'vitest';
import { requiredRoleFor, roleSatisfied, routeManifest } from './routeManifest';

// Minimal RoleSubject-shaped fixtures — deliberately not imported from
// tests/fixtures/personas.ts (outside src/, and outside this workstream's
// vitest include glob) so this file stays self-contained.
const appAdmin = { isAppAdmin: true, orgs: [] };
const orgAdmin = { isAppAdmin: false, orgs: [{ id: 'org-0001', role: 'admin' }] };
const orgEditor = { isAppAdmin: false, orgs: [{ id: 'org-0001', role: 'editor' }] };
const reader = { isAppAdmin: false, orgs: [{ id: 'org-0001', role: 'viewer' }] };
const nobody = { isAppAdmin: false, orgs: [] };

describe('routeManifest requiredRole — mirrors internal/mgmtapi/rpc_interceptor.go', () => {
  it('every admin/* route requires app-admin', () => {
    const adminRoutes = routeManifest.filter((r) => r.path.startsWith('/admin/'));
    expect(adminRoutes.length).toBeGreaterThan(0);
    expect(adminRoutes.every((r) => r.requiredRole === 'app-admin')).toBe(true);
  });

  it('/git and /audit require org-admin (GitOpsService and AuditService)', () => {
    expect(routeManifest.find((r) => r.path === '/git')?.requiredRole).toBe('org-admin');
    expect(routeManifest.find((r) => r.path === '/audit')?.requiredRole).toBe('org-admin');
  });

  it('wizards and pipeline-create require org-editor (WizardService, PipelineService)', () => {
    expect(routeManifest.find((r) => r.path === '/wizards')?.requiredRole).toBe('org-editor');
    expect(routeManifest.find((r) => r.path === '/wizards/$kind')?.requiredRole).toBe('org-editor');
    expect(routeManifest.find((r) => r.path === '/pipelines/new')?.requiredRole).toBe('org-editor');
    expect(requiredRoleFor('/pipelines/visual/new')).toBe('org-editor');
  });

  it('routes with no elevated requirement carry no requiredRole', () => {
    expect(routeManifest.find((r) => r.path === '/')?.requiredRole).toBeUndefined();
    expect(routeManifest.find((r) => r.path === '/collectors')?.requiredRole).toBeUndefined();
  });
});

describe('roleSatisfied', () => {
  it('app-admin bypasses every requirement, even with no org membership', () => {
    expect(roleSatisfied(appAdmin, 'org-0001', 'app-admin')).toBe(true);
    expect(roleSatisfied(appAdmin, 'org-0001', 'org-admin')).toBe(true);
    expect(roleSatisfied(appAdmin, '', 'org-reader')).toBe(true);
  });

  it('org-admin clears org-admin, org-editor and org-reader in its own org', () => {
    expect(roleSatisfied(orgAdmin, 'org-0001', 'org-admin')).toBe(true);
    expect(roleSatisfied(orgAdmin, 'org-0001', 'org-editor')).toBe(true);
    expect(roleSatisfied(orgAdmin, 'org-0001', 'org-reader')).toBe(true);
  });

  it('org-editor clears org-editor and org-reader, not org-admin', () => {
    expect(roleSatisfied(orgEditor, 'org-0001', 'org-editor')).toBe(true);
    expect(roleSatisfied(orgEditor, 'org-0001', 'org-reader')).toBe(true);
    expect(roleSatisfied(orgEditor, 'org-0001', 'org-admin')).toBe(false);
  });

  it('a viewer clears only org-reader — the RED case from the S7 plan', () => {
    expect(roleSatisfied(reader, 'org-0001', 'org-reader')).toBe(true);
    expect(roleSatisfied(reader, 'org-0001', 'org-admin')).toBe(false);
    expect(roleSatisfied(reader, 'org-0001', 'org-editor')).toBe(false);
  });

  it('nobody (no orgs, not app-admin) is denied any org-scoped requirement', () => {
    expect(roleSatisfied(nobody, '', 'org-reader')).toBe(false);
    expect(roleSatisfied(nobody, '', 'app-admin')).toBe(false);
  });

  it('a null/undefined subject (useMe still loading) is always denied', () => {
    expect(roleSatisfied(null, 'org-0001', 'org-reader')).toBe(false);
    expect(roleSatisfied(undefined, 'org-0001', 'org-reader')).toBe(false);
  });
});

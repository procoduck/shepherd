import { describe, expect, it } from 'vitest';
import { Role } from '@/lib/roles';
import { requiredRoleFor, roleSatisfied, routeManifest } from './routeManifest';

// Minimal RoleSubject-shaped fixtures — deliberately not imported from
// tests/fixtures/personas.ts (outside src/, and outside this workstream's
// vitest include glob) so this file stays self-contained.
const appAdmin = { isAppAdmin: true, orgs: [] };
const orgAdmin = { isAppAdmin: false, orgs: [{ id: 'org-0001', role: Role.ADMIN }] };
const orgEditor = { isAppAdmin: false, orgs: [{ id: 'org-0001', role: Role.EDITOR }] };
const viewer = { isAppAdmin: false, orgs: [{ id: 'org-0001', role: Role.VIEWER }] };
const nobody = { isAppAdmin: false, orgs: [] };

describe('routeManifest requiredRole — mirrors internal/mgmtapi/rpc_interceptor.go', () => {
  it('every admin/* route requires app-admin, except the org-admin agent tokens page', () => {
    const adminRoutes = routeManifest.filter(
      (r) => r.path.startsWith('/admin/') && r.path !== '/admin/tokens',
    );
    expect(adminRoutes.length).toBeGreaterThan(0);
    expect(adminRoutes.every((r) => r.requiredRole === 'app-admin')).toBe(true);
  });

  it('/admin/tokens requires org admin (ListAgentTokens: app admin or org admin)', () => {
    expect(requiredRoleFor('/admin/tokens')).toBe(Role.ADMIN);
  });

  it('/git and /audit require admin (GitOpsService and AuditService)', () => {
    expect(routeManifest.find((r) => r.path === '/git')?.requiredRole).toBe(Role.ADMIN);
    expect(routeManifest.find((r) => r.path === '/audit')?.requiredRole).toBe(Role.ADMIN);
  });

  it('wizards and pipeline-create require editor (WizardService, PipelineService)', () => {
    expect(routeManifest.find((r) => r.path === '/wizards')?.requiredRole).toBe(Role.EDITOR);
    expect(routeManifest.find((r) => r.path === '/wizards/$kind')?.requiredRole).toBe(Role.EDITOR);
    expect(routeManifest.find((r) => r.path === '/pipelines/new')?.requiredRole).toBe(Role.EDITOR);
    expect(requiredRoleFor('/pipelines/visual/new')).toBe(Role.EDITOR);
  });

  it('routes with no elevated requirement carry no requiredRole', () => {
    expect(routeManifest.find((r) => r.path === '/')?.requiredRole).toBeUndefined();
    expect(routeManifest.find((r) => r.path === '/collectors')?.requiredRole).toBeUndefined();
  });
});

describe('roleSatisfied', () => {
  it('app-admin bypasses every requirement, even with no org membership', () => {
    expect(roleSatisfied(appAdmin, 'org-0001', 'app-admin')).toBe(true);
    expect(roleSatisfied(appAdmin, 'org-0001', Role.ADMIN)).toBe(true);
    expect(roleSatisfied(appAdmin, '', Role.VIEWER)).toBe(true);
  });

  it('admin clears admin, editor and viewer in its own org', () => {
    expect(roleSatisfied(orgAdmin, 'org-0001', Role.ADMIN)).toBe(true);
    expect(roleSatisfied(orgAdmin, 'org-0001', Role.EDITOR)).toBe(true);
    expect(roleSatisfied(orgAdmin, 'org-0001', Role.VIEWER)).toBe(true);
  });

  it('editor clears editor and viewer, not admin', () => {
    expect(roleSatisfied(orgEditor, 'org-0001', Role.EDITOR)).toBe(true);
    expect(roleSatisfied(orgEditor, 'org-0001', Role.VIEWER)).toBe(true);
    expect(roleSatisfied(orgEditor, 'org-0001', Role.ADMIN)).toBe(false);
  });

  it('a viewer clears only the viewer floor — the RED case from the S7 plan', () => {
    expect(roleSatisfied(viewer, 'org-0001', Role.VIEWER)).toBe(true);
    expect(roleSatisfied(viewer, 'org-0001', Role.ADMIN)).toBe(false);
    expect(roleSatisfied(viewer, 'org-0001', Role.EDITOR)).toBe(false);
  });

  it('nobody (no orgs, not app-admin) is denied any org-scoped requirement', () => {
    expect(roleSatisfied(nobody, '', Role.VIEWER)).toBe(false);
    expect(roleSatisfied(nobody, '', 'app-admin')).toBe(false);
  });

  it('a null/undefined subject (useMe still loading) is always denied', () => {
    expect(roleSatisfied(null, 'org-0001', Role.VIEWER)).toBe(false);
    expect(roleSatisfied(undefined, 'org-0001', Role.VIEWER)).toBe(false);
  });
});

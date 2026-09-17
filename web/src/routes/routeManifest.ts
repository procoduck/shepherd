// routeManifest.ts — single source of truth for route tags.
// The protected-routes spec imports this to build its test matrix.
// Every route MUST have a tag. Missing tags fail the spec's completeness guard.

/**
 * Client-side role floor for a route, mirroring
 * internal/mgmtapi/rpc_interceptor.go's per-procedure requirement for the
 * route's primary read RPC:
 *   - 'app-admin' — admin/*  (AdminService, UserService: app admin only)
 *   - 'org-admin' — /git, /audit (GitOpsService, AuditService)
 *   - 'org-editor' — /wizards, /wizards/$kind, /pipelines/new
 *     (WizardService, and pipeline authoring)
 *   - 'org-reader' — every other org-scoped page that has no elevated
 *     requirement of its own (e.g. /teams — TeamService.ListTeams)
 * The server remains the actual enforcement (RequireRole exists only so a
 * denied direct navigation never mounts the page and fires its RPC).
 */
export type RequiredRole = 'app-admin' | 'org-admin' | 'org-editor' | 'org-reader';

export interface RouteEntry {
  path: string;
  tag: 'public' | 'protected';
  /** A locator string that should be unique to this page when authenticated */
  distinctLocator?: string;
  requiredRole?: RequiredRole;
  /** Human breadcrumb label for this route's own segment (breadcrumb.ts).
   *  A route with no label falls back to a title-cased raw path segment. */
  label?: string;
}

export const routeManifest: RouteEntry[] = [
  { path: '/login', tag: 'public' },
  // Reachable while signed in but blocked on a password change, so it cannot
  // sit behind the session guard; public like /login.
  { path: '/change-password', tag: 'public' },
  { path: '/', tag: 'protected', distinctLocator: 'text=Overview', label: 'Overview' },
  {
    path: '/collectors',
    tag: 'protected',
    distinctLocator: 'text=Collectors',
    label: 'Collectors',
  },
  {
    path: '/collectors/$id',
    tag: 'protected',
    distinctLocator: 'text=Collector',
    label: 'Collector',
  },
  { path: '/pipelines', tag: 'protected', distinctLocator: 'text=Pipelines', label: 'Pipelines' },
  {
    path: '/pipelines/new',
    tag: 'protected',
    requiredRole: 'org-editor',
    label: 'New pipeline',
  },
  { path: '/pipelines/$id', tag: 'protected', label: 'Pipeline' },
  {
    path: '/destinations',
    tag: 'protected',
    distinctLocator: 'text=Destinations',
    label: 'Destinations',
  },
  {
    path: '/teams',
    tag: 'protected',
    distinctLocator: 'text=Teams',
    requiredRole: 'org-reader',
    label: 'Teams',
  },
  {
    path: '/tenant-routes',
    tag: 'protected',
    distinctLocator: 'text=Tenant routes',
    requiredRole: 'org-reader',
    label: 'Tenant routes',
  },
  {
    path: '/service-accounts',
    tag: 'protected',
    distinctLocator: 'text=Service accounts',
    requiredRole: 'org-admin',
    label: 'Service accounts',
  },
  {
    path: '/git',
    tag: 'protected',
    distinctLocator: 'text=Git sync',
    requiredRole: 'org-admin',
    label: 'Git sync',
  },
  // Full-bleed canvas routes. They bypass contentRoute (see router.tsx) but are
  // still protected, and were missing here entirely -- which the completeness
  // guard could not notice, because it only checked that listed routes had a
  // tag rather than that every real route was listed.
  { path: '/pipelines/visual/new', tag: 'protected', label: 'Visual builder' },
  { path: '/pipelines/$id/visual', tag: 'protected', label: 'Visual builder' },
  { path: '/pipelines/$id/graph', tag: 'protected', label: 'Graph view' },
  {
    path: '/wizards',
    tag: 'protected',
    distinctLocator: 'text=Wizards',
    requiredRole: 'org-editor',
    label: 'Wizards',
  },
  {
    path: '/wizards/$kind',
    tag: 'protected',
    requiredRole: 'org-editor',
    label: 'Wizard',
  },
  {
    path: '/admin/orgs',
    tag: 'protected',
    distinctLocator: 'text=Organisations',
    requiredRole: 'app-admin',
    label: 'Organisations',
  },
  {
    path: '/admin/clusters',
    tag: 'protected',
    distinctLocator: 'text=Clusters',
    requiredRole: 'app-admin',
    label: 'Clusters',
  },
  {
    path: '/admin/tokens',
    tag: 'protected',
    distinctLocator: 'text=Agent Tokens',
    requiredRole: 'app-admin',
    label: 'Agent Tokens',
  },
  {
    path: '/admin/users',
    tag: 'protected',
    distinctLocator: 'text=Users',
    requiredRole: 'app-admin',
    label: 'Users',
  },
  {
    path: '/admin/auth',
    tag: 'protected',
    distinctLocator: 'text=Single sign-on',
    requiredRole: 'app-admin',
    label: 'Single sign-on',
  },
  {
    path: '/audit',
    tag: 'protected',
    distinctLocator: 'text=Audit log',
    requiredRole: 'org-admin',
    label: 'Audit log',
  },
];

/** The minimal shape of shepherd.mgmt.v1.MeService/GetMe's response that a
 *  role decision needs. Matches GetMeResponse (web/src/gen/.../me_pb.ts) and
 *  tests/fixtures/personas.ts's MeResponse — a structural subset rather than
 *  an import from either, so routeManifest has no dependency on generated
 *  proto code or the Playwright fixtures directory. */
export interface RoleSubject {
  isAppAdmin: boolean;
  orgs: Array<{ id: string; role: string }>;
}

// org_members.role values (Grafana's naming, per internal/auth/authz.go's
// localToRequirement) ranked so a floor can be compared numerically. Higher
// is more capable; org admin satisfies every floor.
const ORG_ROLE_RANK: Record<string, number> = { admin: 3, editor: 2, viewer: 1 };

const REQUIRED_ORG_ROLE_RANK: Record<'org-admin' | 'org-editor' | 'org-reader', number> = {
  'org-admin': 3,
  'org-editor': 2,
  'org-reader': 1,
};

/**
 * Whether `me` clears `required` for the org named by `orgId`, mirroring
 * internal/auth/authz.go's Authorize: app admin short-circuits every
 * requirement (including 'app-admin' itself) regardless of org membership;
 * every other requirement needs membership in `orgId` at or above its rank.
 * A missing subject (useMe still loading, or genuinely signed out) is always
 * denied — callers must not evaluate this before useMe resolves.
 */
export function roleSatisfied(
  me: RoleSubject | null | undefined,
  orgId: string,
  required: RequiredRole,
): boolean {
  if (!me) return false;
  if (me.isAppAdmin) return true;
  if (required === 'app-admin') return false;
  const membership = me.orgs.find((o) => o.id === orgId);
  if (!membership) return false;
  return (ORG_ROLE_RANK[membership.role] ?? 0) >= REQUIRED_ORG_ROLE_RANK[required];
}

// Agent tokens are bound to an organisation (migration 0038). These helpers
// hold the page's scoping and display rules as plain functions, so they are
// testable without rendering the page.

export interface OrgOption {
  id: string;
  name: string;
  displayName: string;
}

/**
 * The org_id a ListAgentTokens / RevokeAgentToken call is scoped to. An app
 * admin calls unscoped ('') and sees every token, unbound ones included; an
 * org admin is scoped to the org they are looking at — the server refuses
 * them anything else.
 */
export function tokenScope(isAppAdmin: boolean, orgId: string): string {
  return isAppAdmin ? '' : orgId;
}

/** Whether a token has no organisation binding (the agent API refuses it). */
export function isUnbound(token: { orgId: string }): boolean {
  return token.orgId === '';
}

/**
 * Live, unbound tokens first — they need an app admin to bind them — then the
 * server's order. The server already sorts this way; the page re-sorts so a
 * token bound or revoked in place does not jump around before the refetch.
 */
export function sortTokens<T extends { orgId: string; status: string }>(items: readonly T[]): T[] {
  const needsBinding = (t: T) => isUnbound(t) && t.status === 'active';
  return [...items].sort((a, b) => Number(needsBinding(b)) - Number(needsBinding(a)));
}

/** A comma/space-separated allowlist as typed into a form. */
export function splitAllowlist(s: string): string[] {
  return s
    .split(/[,\s]+/)
    .map((v) => v.trim())
    .filter(Boolean);
}

/**
 * The organisations a token may be bound to from the create/bind dialogs: for
 * an app admin every org (the ListOrgs result) plus the orgs on their own
 * session, deduplicated by id; for an org admin only the org they are in.
 */
export function bindableOrgs(
  isAppAdmin: boolean,
  orgId: string,
  sessionOrgs: readonly OrgOption[],
  allOrgs: readonly OrgOption[],
): OrgOption[] {
  if (!isAppAdmin) return sessionOrgs.filter((o) => o.id === orgId);
  const seen = new Set<string>();
  const out: OrgOption[] = [];
  for (const o of [...allOrgs, ...sessionOrgs]) {
    if (seen.has(o.id)) continue;
    seen.add(o.id);
    out.push(o);
  }
  return out;
}

/** "Display Name (slug)", or just the slug when they are the same. */
export function orgLabel(o: OrgOption): string {
  return o.displayName && o.displayName !== o.name ? `${o.displayName} (${o.name})` : o.name;
}

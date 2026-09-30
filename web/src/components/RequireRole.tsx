import { Link } from '@tanstack/react-router';
import { Lock } from 'lucide-react';
import type { ReactNode } from 'react';
import { useMe } from '@/hooks/useMe';
import { useOrg } from '@/hooks/useOrg';
import { type RequiredRole, roleSatisfied } from '@/routes/routeManifest';

// What each floor means in words a user recognises — the denial page names the
// role the page needs rather than an internal requirement id.
const REQUIRED_ROLE_LABEL: Record<RequiredRole, string> = {
  'app-admin': 'an application administrator',
  'org-admin': 'an admin of this organisation',
  'org-editor': 'an editor or admin of this organisation',
  'org-reader': 'a member of this organisation',
};

/**
 * Client-side gate for routeManifest's `requiredRole`. The server remains
 * the real enforcement (internal/mgmtapi/rpc_interceptor.go) — this exists
 * so a denied direct navigation never mounts the page component at all, i.e.
 * its privileged RPC never fires, instead of letting the page render and
 * then fail loudly (or, worse, render successfully because a mock or a
 * client-only check was the only thing standing in the way).
 *
 * Renders nothing until useMe resolves — no flash of a page the viewer
 * isn't allowed to see. Once resolved, a denial renders a
 * `data-testid="route-denied"` page saying so, at the URL that was asked for.
 * It used to toast and redirect to '/', which read as the link being broken
 * (#206): the toast was easy to miss and the user just landed on Overview.
 * The shell's nav hides links a role cannot use, so this page is what a
 * direct URL, a bookmark or an org switch lands on.
 */
export function RequireRole({
  requiredRole,
  children,
}: {
  requiredRole: RequiredRole;
  children: ReactNode;
}) {
  const { data: me, isLoading } = useMe();
  const { orgId } = useOrg();

  if (isLoading) return null;
  if (!roleSatisfied(me, orgId, requiredRole)) {
    return (
      <div className='p-6'>
        <div
          role='alert'
          data-testid='route-denied'
          className='mx-auto mt-12 max-w-md rounded-lg border border-border bg-card p-6 text-center space-y-3'
        >
          <Lock size={20} className='mx-auto text-muted' aria-hidden='true' />
          <h1 className='text-lg font-semibold'>You don't have access to this page</h1>
          <p className='text-sm text-muted'>
            This page is only available to {REQUIRED_ROLE_LABEL[requiredRole]}. Ask an administrator
            if you need access.
          </p>
          <Link to='/' className='inline-block text-sm text-indigo-400 hover:underline'>
            Back to Overview
          </Link>
        </div>
      </div>
    );
  }
  return <>{children}</>;
}

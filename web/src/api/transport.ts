// Shared Connect RPC transport for the shepherd.mgmt.v1 management API
// (docs/archive/api-contract-design.md). One transport instance backs every
// generated service client:
//  - same-origin credentials, so the session cookie rides along;
//  - an interceptor adding X-Requested-With, which the server's CSRF
//    middleware requires on every mutating (i.e. every Connect) request
//    (internal/auth/auth.go: CSRFMiddleware);
//  - a helper normalizing any thrown ConnectError into the app's existing
//    ApiError shape ({ code, message }), matching the legacy REST client.

import type { DescService } from '@bufbuild/protobuf';
import {
  type Client,
  Code,
  ConnectError,
  createClient,
  type Interceptor,
} from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-web';
import { AdminService } from '@/gen/shepherd/mgmt/v1/admin_pb';
import { AuditService } from '@/gen/shepherd/mgmt/v1/audit_pb';
import { DestinationService } from '@/gen/shepherd/mgmt/v1/destination_pb';
import { FleetService } from '@/gen/shepherd/mgmt/v1/fleet_pb';
import { GitOpsService } from '@/gen/shepherd/mgmt/v1/gitops_pb';
import { MeService } from '@/gen/shepherd/mgmt/v1/me_pb';
import { PipelineService } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { SimulateService } from '@/gen/shepherd/mgmt/v1/simulate_pb';
import { TeamService } from '@/gen/shepherd/mgmt/v1/team_pb';
import { UserService } from '@/gen/shepherd/mgmt/v1/user_pb';
import { VisualService } from '@/gen/shepherd/mgmt/v1/visual_pb';
import { WizardService } from '@/gen/shepherd/mgmt/v1/wizard_pb';
import type { ApiError } from './client';

/** Adds the header the server's CSRF middleware requires on every Connect call. */
const csrfInterceptor: Interceptor = (next) => async (req) => {
  req.header.set('X-Requested-With', 'XMLHttpRequest');
  return next(req);
};

/** Shared transport for every shepherd.mgmt.v1 service client. */
export const transport = createConnectTransport({
  baseUrl: '/',
  fetch: (input, init) => fetch(input, { ...init, credentials: 'same-origin' }),
  interceptors: [csrfInterceptor],
});

/**
 * Converts any value thrown by a Connect call (normally a ConnectError) into
 * the app's existing ApiError shape, so callers don't need to know whether
 * they're talking to the REST shim or the Connect endpoint directly.
 */
export function toApiError(reason: unknown): ApiError {
  const err = ConnectError.from(reason);
  return { code: connectCodeName(err.code), message: err.rawMessage || err.message };
}

/** "PermissionDenied" -> "permission_denied", matching the REST error envelope's code style. */
function connectCodeName(code: Code): string {
  return Code[code].replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase();
}

/** One createClient(service, transport) call per shepherd.mgmt.v1 service. */
export const clients = {
  me: createClient(MeService, transport),
  admin: createClient(AdminService, transport),
  user: createClient(UserService, transport),
  fleet: createClient(FleetService, transport),
  pipeline: createClient(PipelineService, transport),
  destination: createClient(DestinationService, transport),
  gitOps: createClient(GitOpsService, transport),
  wizard: createClient(WizardService, transport),
  visual: createClient(VisualService, transport),
  simulate: createClient(SimulateService, transport),
  team: createClient(TeamService, transport),
  audit: createClient(AuditService, transport),
} satisfies Record<string, Client<DescService>>;

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

// Spec-drift guard (docs/frontend-testing.md §3.1 / §10): every endpoint the app can call
// must have a default handler in web/tests/mocks/handlers.ts, or be explicitly,
// deliberately excused below.
//
// Two sources, both authoritative for their half of the surface:
//   - Connect procedures: read from the generated service descriptors
//     (web/src/gen/shepherd/mgmt/v1/*_pb.ts), so a procedure added to proto/ is covered the
//     moment `make generate` runs — nobody has to remember to list it here.
//   - The few REST routes the Connect contract deliberately leaves out: parsed from the
//     fenced block in docs/spec.md §12 ("What /api still serves").
//
// Until v0.11.0 this test parsed spec §12's list of /api REST shim routes and mapped each to
// its Connect procedure. The shim was removed; checking the descriptors directly is stricter,
// because it also covers procedures the old REST list never named.

const specPath = path.resolve(__dirname, '../../docs/spec.md');
const handlersPath = path.resolve(__dirname, '../tests/mocks/handlers.ts');

const generated = import.meta.glob('/src/gen/shepherd/mgmt/v1/*_pb.ts', { eager: true }) as Record<
  string,
  Record<string, unknown>
>;

interface ServiceDesc {
  kind: string;
  typeName: string;
  methods: { name: string }[];
}

/** Every `/shepherd.mgmt.v1.<Service>/<Method>` procedure path the generated code defines. */
function connectProcedures(): string[] {
  const procedures: string[] = [];
  for (const mod of Object.values(generated)) {
    for (const value of Object.values(mod)) {
      const desc = value as Partial<ServiceDesc> | null;
      if (desc?.kind !== 'service' || !desc.typeName) continue;
      for (const method of desc.methods ?? []) {
        procedures.push(`/${desc.typeName}/${method.name}`);
      }
    }
  }
  return procedures.sort();
}

/** Every `router.register('METHOD', 'path', ...)` call in handlers.ts, as "METHOD path" keys. */
function registeredRouteKeys(handlersSource: string): Set<string> {
  const keys = new Set<string>();
  const call = /router\.register\(\s*'([A-Z*]+)'\s*,\s*'([^']+)'/g;
  for (const m of handlersSource.matchAll(call)) {
    keys.add(`${m[1]} ${m[2]}`);
  }
  return keys;
}

interface RestEndpoint {
  method: string;
  path: string;
  /** original spec line, for failure messages */ raw: string;
}

/** Parses the fenced REST route list out of spec.md §12. */
function parseSpecRestRoutes(specSource: string): RestEndpoint[] {
  const heading = '## 12. Management API';
  const headingIdx = specSource.indexOf(heading);
  if (headingIdx === -1) {
    throw new Error(`spec.md heading "${heading}" not found -- has §12 been renamed/moved?`);
  }
  const nextSection = specSource.indexOf('\n## ', headingIdx + heading.length);
  const section = specSource.slice(headingIdx, nextSection === -1 ? undefined : nextSection);
  const fenceStart = section.indexOf('```');
  const fenceEnd = section.indexOf('```', fenceStart + 3);
  if (fenceStart === -1 || fenceEnd === -1) {
    throw new Error('spec.md §12 REST route block not found (expected a ``` fenced list)');
  }
  const endpoints: RestEndpoint[] = [];
  for (const rawLine of section.slice(fenceStart + 3, fenceEnd).split('\n')) {
    const line = rawLine.trim();
    const match = /^(GET|POST|PUT|PATCH|DELETE)\s+(.+?)(\s{2,}|$)/.exec(line);
    if (!match) continue;
    // "POST /a, /b" lists several paths sharing one method.
    for (const p of match[2].split(',')) {
      endpoints.push({ method: match[1], path: p.trim(), raw: line });
    }
  }
  return endpoints;
}

// Procedures with no default mock handler. Every entry predates this guard's switch to the
// generated descriptors (the REST-list version never looked at them), so each is a known
// backlog item, not an oversight this test excuses. Delete an entry in the same change that
// adds its handler; a stale entry fails the test below.
const UNMOCKED_PROCEDURES: Record<string, string> = {
  '/shepherd.mgmt.v1.DestinationService/ListDestinationBindings':
    'destination bindings: no default handler yet',
  '/shepherd.mgmt.v1.DestinationService/GetDestinationBinding':
    'destination bindings: no default handler yet',
  '/shepherd.mgmt.v1.DestinationService/CreateDestinationBinding':
    'destination bindings: no default handler yet',
  '/shepherd.mgmt.v1.DestinationService/UpdateDestinationBinding':
    'destination bindings: no default handler yet',
  '/shepherd.mgmt.v1.DestinationService/DeleteDestinationBinding':
    'destination bindings: no default handler yet',
  '/shepherd.mgmt.v1.DestinationService/ResolveDestinationBinding':
    'destination bindings: no default handler yet',
  '/shepherd.mgmt.v1.FleetService/SetCollectorLabel': 'collector labels: no default handler yet',
  '/shepherd.mgmt.v1.FleetService/DeleteCollectorLabel': 'collector labels: no default handler yet',
  '/shepherd.mgmt.v1.PipelineService/SetPipelineOwner':
    'pipeline ownership: no default handler yet',
  '/shepherd.mgmt.v1.UserService/ListOrgMembers': 'org members list: no default handler yet',
};

// Spec §12 REST routes the mock layer deliberately does not serve.
const UNMOCKED_REST: Record<string, string> = {
  'GET /api/version': 'build version; the SPA never calls it',
};

// The mock router matches `:param` segments, so a spec path is served by a handler whose
// pattern matches it segment-for-segment (`/api/schema/current` by `/api/schema/:version`).
function servedBy(pattern: string, specPath: string): boolean {
  const a = pattern.split('/');
  const b = specPath.split('/');
  if (a.length !== b.length) return false;
  return a.every((seg, i) => seg.startsWith(':') || /^\{.+\}$/.test(b[i]) || seg === b[i]);
}

describe('mock coverage of the management API', () => {
  const handlersSource = readFileSync(handlersPath, 'utf8');
  const registered = registeredRouteKeys(handlersSource);
  const procedures = connectProcedures();

  it('finds the generated services (guards against a silently empty glob)', () => {
    expect(procedures.length).toBeGreaterThan(50);
  });

  it('every Connect procedure has a default mock handler, or is excused', () => {
    const missing = procedures.filter(
      (p) => !registered.has(`POST ${p}`) && !(p in UNMOCKED_PROCEDURES),
    );
    expect(missing, 'procedures with no mock handler in tests/mocks/handlers.ts').toEqual([]);
  });

  it('no excused procedure is stale (gone from the contract, or mocked after all)', () => {
    const stale = Object.keys(UNMOCKED_PROCEDURES).filter(
      (p) => !procedures.includes(p) || registered.has(`POST ${p}`),
    );
    expect(stale, 'remove these from UNMOCKED_PROCEDURES').toEqual([]);
  });

  it('every REST route spec §12 still documents has a mock handler, or is excused', () => {
    const spec = parseSpecRestRoutes(readFileSync(specPath, 'utf8'));
    expect(spec.length, 'spec §12 REST route block parsed empty').toBeGreaterThan(0);
    const patterns = [...registered].map((k) => k.split(' ') as [string, string]);
    const missing = spec
      .filter((e) => !(`${e.method} ${e.path}` in UNMOCKED_REST))
      .filter((e) => !patterns.some(([m, p]) => m === e.method && servedBy(p, e.path)))
      .map((e) => `${e.method} ${e.path}  (spec: ${e.raw})`);
    expect(missing, 'spec §12 REST routes with no mock handler').toEqual([]);
  });
});

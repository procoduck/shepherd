import type { OrgStrictComponent } from '@/gen/shepherd/mgmt/v1/admin_pb';

// A built-in Alloy component name (internal/validate.CheckAllowableComponent):
// two or more dot-separated parts of lower-case letters, digits and
// underscores, each starting with a letter. The server is authoritative — it
// also refuses a component strict tenant mode already accepts — this only
// catches a value that cannot be a component name, before a round trip.
const COMPONENT_NAME = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$/;

/** Why value cannot be allowed as a component, or '' when it may be. */
export function strictComponentError(value: string): string {
  const v = value.trim();
  if (v === '') return 'Enter the component name';
  if (v !== value) return 'The name has leading or trailing spaces';
  if (v.length > 128 || !COMPONENT_NAME.test(v))
    return 'Use a component name such as otelcol.auth.google';
  return '';
}

/** How an entry's shipped classification is shown. */
export function strictComponentClassification(
  c: Pick<OrgStrictComponent, 'classification'>,
): string {
  return c.classification === 'refused' ? 'classified refused' : 'unclassified';
}

import { useMe } from './useMe';

/** Returns the ID of the user's first org, or '' if not loaded/no orgs. */
export function useOrgId(): string {
  const { data: me } = useMe();
  return me?.orgs?.[0]?.id ?? '';
}

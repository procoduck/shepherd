import type { OrgImportRepository } from '@/gen/shepherd/mgmt/v1/admin_pb';

// The shapes AddOrgImportRepository accepts (internal/pinnedimport
// .NormalizeRepository): an http(s):// or ssh:// URL, or the scp-like
// [user@]host:path form. The server is authoritative — it also refuses
// ambiguous spellings (a query, fragment, percent-escape, dot segment) —
// this only catches a value that cannot be a git remote at all, before a
// round trip.
const URL_FORM = /^(https?|ssh):\/\/[^\s/]+\/\S+$/i;
const SCP_FORM = /^([^\s@/:]+@)?[^\s@/:]+:[^\s/]\S*$/;

/** Why value cannot be an allowed import repository, or '' when it may be. */
export function importRepositoryError(value: string): string {
  const v = value.trim();
  if (v === '') return 'Enter the repository URL';
  if (v !== value) return 'The URL has leading or trailing spaces';
  if (URL_FORM.test(v) || (!v.includes('://') && SCP_FORM.test(v))) return '';
  return 'Use https://host/owner/repo.git, ssh://git@host/owner/repo.git or git@host:owner/repo.git';
}

/** How an entry's credential is shown: its name, or "public" for none. */
export function importRepositoryCredential(
  r: Pick<OrgImportRepository, 'credentialId' | 'credentialName'>,
): string {
  if (!r.credentialId) return 'public';
  return r.credentialName || r.credentialId;
}

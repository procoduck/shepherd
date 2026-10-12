import { ConnectError } from '@connectrpc/connect';
import { toApiError } from '@/api/transport';

/**
 * Form error text (#249). Server refusals arrive as Go error strings —
 * lowercase by Go convention ("gateway name is required"). Shown to a person
 * they read as a sentence, so the first letter is raised; the rest is left
 * exactly as the server wrote it (names, quoted values, identifiers).
 */
/** Words a leading field identifier spells as acronyms ("scrape_url" → "Scrape URL"). */
const ACRONYMS: Record<string, string> = {
  id: 'ID',
  url: 'URL',
  ssh: 'SSH',
  dsn: 'DSN',
  oidc: 'OIDC',
  tls: 'TLS',
  ca: 'CA',
  api: 'API',
};

export function sentenceCase(message: string): string {
  const t = message.trim();
  // A message that opens with a field identifier ("tenant_id is in use…")
  // read "Tenant_id is in use…" raised (2026-10-12 walkthrough): the leading
  // identifier is named in words instead, acronyms in capitals.
  const ident = /^([a-z][a-z0-9]*(?:_[a-z0-9]+)+)(?=[\s:,.]|$)/.exec(t)?.[1];
  if (ident) {
    const words = ident.split('_').map((w) => ACRONYMS[w] ?? w);
    const named = words.join(' ');
    return named.charAt(0).toUpperCase() + named.slice(1) + t.slice(ident.length);
  }
  return t.charAt(0).toUpperCase() + t.slice(1);
}

/** The refusal behind `e` as a sentence, or `fallback` when it carries no text. */
export function errorText(e: unknown, fallback: string): string {
  // A refusal with no text would otherwise read as its bare code ("[internal]").
  const message = ConnectError.from(e).rawMessage ? toApiError(e).message : '';
  return sentenceCase(message || fallback);
}

/**
 * The inline error for a form whose submit is a mutation: null while there is
 * none, so it can be passed straight from `mutation.error`. React Query clears
 * the error when the mutation runs again; call `mutation.reset()` when the
 * form closes so reopening it starts clean.
 */
export function formError(e: unknown, fallback: string): string | null {
  return e ? errorText(e, fallback) : null;
}

/**
 * The inline error for a form, placed beside the field it is about when the
 * server says which (M6). The server names the field at the start of an
 * invalid_argument message ("clone URL … is not a git remote URL", "gateway
 * name … is not a valid Kubernetes object name"), so `fields` maps each field
 * key to that leading phrase, matched case-insensitively. A refusal that
 * matches a field is returned under `field[key]` and not as `form`, so it is
 * shown once; anything else (a not-found, a conflict, an unmatched message)
 * stays the form-level error above the buttons.
 */
export function formErrors<K extends string>(
  e: unknown,
  fallback: string,
  fields: Record<K, string>,
): { form: string | null; field: Partial<Record<K, string>> } {
  if (!e) return { form: null, field: {} };
  const { code, message } = toApiError(e);
  if (code === 'invalid_argument' && message) {
    const lower = message.trim().toLowerCase();
    for (const key of Object.keys(fields) as K[]) {
      if (lower.startsWith(fields[key].toLowerCase())) {
        return {
          form: null,
          field: { [key]: sentenceCase(message) } as Partial<Record<K, string>>,
        };
      }
    }
  }
  return { form: errorText(e, fallback), field: {} };
}

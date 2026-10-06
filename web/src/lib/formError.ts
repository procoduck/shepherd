import { ConnectError } from '@connectrpc/connect';
import { toApiError } from '@/api/transport';

/**
 * Form error text (#249). Server refusals arrive as Go error strings —
 * lowercase by Go convention ("gateway name is required"). Shown to a person
 * they read as a sentence, so the first letter is raised; the rest is left
 * exactly as the server wrote it (names, quoted values, identifiers).
 */
export function sentenceCase(message: string): string {
  const t = message.trim();
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

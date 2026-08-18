import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatRelative(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const s = Math.floor(diff / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

export function shortHash(hash: string): string {
  return hash.slice(0, 10);
}

/**
 * Relative-time label for a proto Timestamp field (last_seen, computed_at,
 * etc.) — "unknown" when unset, "just now" under a minute, otherwise
 * minutes/hours/days ago. Mirrors the pre-migration REST-string formatters
 * that lived inline in CollectorsPage/CollectorDetailPage.
 */
export function formatTimestampRelative(ts: Timestamp | undefined): string {
  if (!ts) return 'unknown';
  const diff = Date.now() - timestampDate(ts).getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

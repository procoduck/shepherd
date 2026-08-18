import type { ComponentDef, SchemaPayload, WireTypeDef } from './types';
export async function fetchSchema(version = 'current'): Promise<SchemaPayload> {
  const res = await fetch(`/api/schema/${version}`, {
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  if (!res.ok) throw new Error(`Failed to load schema: ${res.status}`);
  return res.json() as Promise<SchemaPayload>;
}
export function getComponent(schema: SchemaPayload, name: string): ComponentDef | undefined {
  return schema.components[name];
}
export function getCategory(schema: SchemaPayload, name: string): string {
  return schema.components[name]?.category ?? 'advanced';
}
export function getWireTypeDef(schema: SchemaPayload, type: string): WireTypeDef | undefined {
  return schema.wire_types[type];
}
export const WIRE_COLOR: Record<string, string> = {
  targets: 'bg-violet-500',
  'prom.metrics': 'bg-orange-500',
  'loki.logs': 'bg-green-500',
  'otel.traces': 'bg-sky-500',
  'otel.metrics': 'bg-sky-500',
  'otel.logs': 'bg-sky-500',
  'otel.any': 'bg-sky-500',
  'pyroscope.profiles': 'bg-rose-500',
};
export const CATEGORY_BORDER: Record<string, string> = {
  sources: 'border-l-blue-500',
  transform: 'border-l-purple-500',
  destinations: 'border-l-emerald-500',
  config: 'border-l-slate-400',
  advanced: 'border-l-slate-300',
};

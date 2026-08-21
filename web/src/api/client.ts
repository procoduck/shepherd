// Thin bridge between the visual builder's internal (snake_case) domain
// model — web/src/visual/types.ts, shared with the untouched /api/schema/*
// payload — and the generated shepherd.mgmt.v1 Connect clients (camelCase,
// web/src/gen/**). Per docs/archive/api-contract-design.md, CRUD resources migrated
// fully onto generated types (pages import them directly from '@/gen/...');
// what remains here is the conversion boundary for the visual/simulate
// surface, whose consumers (web/src/visual/**) speak the wire-shaped
// snake_case model throughout and are out of scope for this migration.

import type { JsonObject } from '@bufbuild/protobuf';
import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import type { GraphDocument as WireGraphDocument } from '@/gen/shepherd/mgmt/v1/visual_pb';
import type { GraphDocument as LocalGraphDocument } from '@/visual/types';
import { clients } from './transport';

export interface ApiError {
  code: string;
  message: string;
  details?: unknown[];
}

export async function getSchema(
  version = 'current',
): Promise<import('@/visual/types').SchemaPayload> {
  const res = await fetch(`/api/schema/${version}`, {
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  if (!res.ok) throw new Error(`Failed to load schema: ${res.status}`);
  return res.json() as Promise<import('@/visual/types').SchemaPayload>;
}

// ---- Graph document conversion (local snake_case <-> wire camelCase) ----

function toWireGraph(doc: LocalGraphDocument) {
  return {
    kind: doc.kind,
    schemaVersion: doc.schema_version,
    nodes: doc.nodes.map((n) => ({
      id: n.id,
      component: n.component,
      label: n.label,
      position: n.position,
      props: n.props as unknown as JsonObject,
      disabled: n.disabled,
      notes: n.notes,
    })),
    edges: doc.edges.map((e) => ({
      id: e.id,
      from: e.from,
      to: e.to,
      order: e.order,
    })),
    bindings: doc.bindings.map((b) => ({
      node: b.node,
      prop: b.prop,
      ref: b.ref,
    })),
    viewport: doc.viewport,
    meta: doc.meta ? { createdWith: doc.meta.created_with } : undefined,
  };
}

function fromWireGraph(g: WireGraphDocument | undefined): LocalGraphDocument {
  return {
    kind: (g?.kind ?? 'alloy-graph/v1') as LocalGraphDocument['kind'],
    schema_version: g?.schemaVersion ?? '',
    nodes: (g?.nodes ?? []).map((n) => ({
      id: n.id,
      component: n.component,
      label: n.label,
      position: n.position ? { x: n.position.x, y: n.position.y } : { x: 0, y: 0 },
      props: (n.props ?? {}) as Record<string, unknown>,
      disabled: n.disabled,
      notes: n.notes,
    })),
    edges: (g?.edges ?? []).map((e) => ({
      id: e.id,
      from: e.from ? { node: e.from.node, port: e.from.port } : { node: '', port: '' },
      to: e.to ? { node: e.to.node, port: e.to.port } : { node: '', port: '' },
      order: e.order,
    })),
    bindings: (g?.bindings ?? []).map((b) => ({
      node: b.node,
      prop: b.prop,
      ref: b.ref
        ? { node: b.ref.node, export: b.ref.export, expr: b.ref.expr }
        : { node: '', export: '', expr: '' },
    })),
    viewport: g?.viewport
      ? { x: g.viewport.x, y: g.viewport.y, zoom: g.viewport.zoom }
      : { x: 0, y: 0, zoom: 1 },
    meta: { created_with: g?.meta?.createdWith ?? '' },
  };
}

// ---- VisualService ----

export interface VisualRenderResult {
  content: string;
  node_map: Record<string, { start_line: number; end_line: number }>;
  diagnostics: unknown[];
}

export async function renderVisual(
  orgId: string,
  graph: LocalGraphDocument,
): Promise<VisualRenderResult> {
  const res = await clients.visual.render({ orgId, graph: toWireGraph(graph) });
  const nodeMap: Record<string, { start_line: number; end_line: number }> = {};
  for (const [key, value] of Object.entries(res.nodeMap)) {
    nodeMap[key] = { start_line: value.startLine, end_line: value.endLine };
  }
  return { content: res.content, node_map: nodeMap, diagnostics: res.diagnostics };
}

export async function validateVisual(
  orgId: string,
  graph: LocalGraphDocument,
): Promise<{ diagnostics: unknown[] }> {
  const res = await clients.visual.validate({ orgId, graph: toWireGraph(graph) });
  return { diagnostics: res.diagnostics };
}

export type DiffClass =
  | 'component_removed'
  | 'attr_removed'
  | 'attr_added_required'
  | 'enum_value_removed'
  | 'port_type_changed'
  | 'stability_changed'
  | 'migration_available';

export interface UpgradeItem {
  node_id: string;
  node_label: string;
  component: string;
  class: DiffClass;
  detail?: string;
}

export interface UpgradeCheckResult {
  old_version: string;
  new_version: string;
  items: UpgradeItem[];
  needs_upgrade: boolean;
}

export async function upgradeCheck(
  orgId: string,
  graph: LocalGraphDocument,
): Promise<UpgradeCheckResult> {
  const res = await clients.visual.upgradeCheck({ orgId, graph: toWireGraph(graph) });
  return {
    old_version: res.oldVersion,
    new_version: res.newVersion,
    needs_upgrade: res.needsUpgrade,
    items: res.items.map((i) => ({
      node_id: i.nodeId,
      node_label: i.nodeLabel,
      component: i.component,
      class: i.class as DiffClass,
      detail: i.detail,
    })),
  };
}

export function stampSchemaVersion(graph: LocalGraphDocument, version: string): LocalGraphDocument {
  return { ...graph, schema_version: version };
}

export interface GraphViewResult {
  graph: LocalGraphDocument;
  opaque: boolean;
  warning: string;
}

export async function graphView(orgId: string, id: string): Promise<GraphViewResult> {
  const res = await clients.visual.graphView({ orgId, id });
  return { graph: fromWireGraph(res.graph), opaque: res.opaque, warning: res.warning };
}

// ---- SimulateService ----

export interface RelabelStep {
  rule_index: number;
  action: string;
  before: Record<string, string>;
  after?: Record<string, string>;
  kept: boolean;
}
export interface TargetTrace {
  input: Record<string, string>;
  steps: RelabelStep[];
  output?: Record<string, string>;
  kept: boolean;
}
export interface SimulateRelabelResult {
  traces: TargetTrace[];
}
export interface StageEffect {
  stage_index: number;
  stage_type: string;
  simulated: boolean;
  line_before: string;
  line_after: string;
  labels_before: Record<string, string>;
  labels_after: Record<string, string>;
  dropped?: boolean;
  note?: string;
}
export interface LineTrace {
  input: string;
  steps: StageEffect[];
  output?: string;
  dropped: boolean;
}
export interface SimulateLogsResult {
  traces: LineTrace[];
}

function toWireRelabelRule(rule: Record<string, unknown>) {
  return {
    sourceLabels: (rule.source_labels as string[] | undefined) ?? [],
    separator: (rule.separator as string | undefined) ?? '',
    regex: (rule.regex as string | undefined) ?? '',
    targetLabel: (rule.target_label as string | undefined) ?? '',
    replacement: (rule.replacement as string | undefined) ?? '',
    action: (rule.action as string | undefined) ?? '',
    modulus: BigInt((rule.modulus as number | string | bigint | undefined) ?? 0),
  };
}

export async function simulateRelabel(
  orgId: string,
  body: { rules: unknown[]; sample_targets: unknown[] },
): Promise<SimulateRelabelResult> {
  const res = await clients.simulate.simulateRelabel({
    orgId,
    rules: body.rules.map((r) => toWireRelabelRule(r as Record<string, unknown>)),
    sampleTargets: body.sample_targets as JsonObject[],
  });
  return {
    traces: res.traces.map((t) => ({
      input: t.input,
      output: t.output,
      kept: t.kept,
      steps: t.steps.map((step) => ({
        rule_index: step.ruleIndex,
        action: step.action,
        before: step.before,
        after: step.after,
        kept: step.kept,
      })),
    })),
  };
}

function toWireStageSpec(stage: Record<string, unknown>) {
  return {
    type: (stage.type as string | undefined) ?? '',
    expressions: (stage.expressions as Record<string, string> | undefined) ?? {},
    source: (stage.source as string | undefined) ?? '',
    separator: (stage.separator as string | undefined) ?? '',
    expression: (stage.expression as string | undefined) ?? '',
    labels: (stage.labels as Record<string, string> | undefined) ?? {},
    dropLabels: (stage.drop_labels as string[] | undefined) ?? [],
    dropValue: (stage.drop_value as string | undefined) ?? '',
    template: (stage.template as string | undefined) ?? '',
    firstline: (stage.firstline as string | undefined) ?? '',
  };
}

export async function simulateLogs(
  orgId: string,
  body: { stages: unknown[]; sample_lines: string[] },
): Promise<SimulateLogsResult> {
  const res = await clients.simulate.simulateLogs({
    orgId,
    stages: body.stages.map((s) => toWireStageSpec(s as Record<string, unknown>)),
    sampleLines: body.sample_lines,
  });
  return {
    traces: res.traces.map((t) => ({
      input: t.input,
      output: t.output,
      dropped: t.dropped,
      steps: t.steps.map((step) => ({
        stage_index: step.stageIndex,
        stage_type: step.stageType,
        simulated: step.simulated,
        line_before: step.lineBefore,
        line_after: step.lineAfter,
        labels_before: step.labelsBefore,
        labels_after: step.labelsAfter,
        dropped: step.dropped,
        note: step.note,
      })),
    })),
  };
}

// ---- SimulateService: S3 sandbox run (VB-1 design doc §6.4) ----

export interface SimRewrite {
  node_id: string;
  node_label: string;
  component: string;
  kind: string;
  detail: string;
}
export interface CapturedSeriesItem {
  name: string;
  labels: Record<string, string>;
  sample_count: number;
}
export interface CapturedLogLineItem {
  labels: Record<string, string>;
  line: string;
}
export interface ComponentHealthItem {
  node_id: string;
  node_label: string;
  component: string;
  health_state: string;
  message: string;
}

/** Mirrors shepherd.mgmt.v1.SimulateRun. `status` is one of queued | running |
 * completed | failed | expired; `error_code` (set iff status === 'failed')
 * mirrors internal/simulate.RunError. */
export interface SimulateRunResult {
  id: string;
  org_id: string;
  status: string;
  created_at?: string;
  started_at?: string;
  finished_at?: string;
  requested_duration_seconds: number;
  queue_position: number;
  rewrites: SimRewrite[];
  captured_series: CapturedSeriesItem[];
  captured_log_lines: CapturedLogLineItem[];
  component_health: ComponentHealthItem[];
  gate_diagnostics: unknown[];
  stderr_tail: string;
  error_code: string;
  error_message: string;
  fidelity_note: string;
}

/** CreateRun only ever returns the run id (design doc §2: "graph -> {
 * run_id }") — callers must poll getSandboxRun for state, never trust
 * anything else off this response. */
export async function createSandboxRun(
  orgId: string,
  graph: LocalGraphDocument,
  durationSeconds = 30,
): Promise<{ run_id: string }> {
  const res = await clients.simulate.createRun({
    orgId,
    graph: toWireGraph(graph),
    durationSeconds,
  });
  return { run_id: res.runId };
}

function tsToIso(ts: Timestamp | undefined): string | undefined {
  return ts ? timestampDate(ts).toISOString() : undefined;
}

export async function getSandboxRun(orgId: string, id: string): Promise<SimulateRunResult> {
  const res = await clients.simulate.getRun({ orgId, id });
  return {
    id: res.id,
    org_id: res.orgId,
    status: res.status,
    created_at: tsToIso(res.createdAt),
    started_at: tsToIso(res.startedAt),
    finished_at: tsToIso(res.finishedAt),
    requested_duration_seconds: res.requestedDurationSeconds,
    queue_position: res.queuePosition,
    rewrites: res.rewrites.map((r) => ({
      node_id: r.nodeId,
      node_label: r.nodeLabel,
      component: r.component,
      kind: r.kind,
      detail: r.detail,
    })),
    captured_series: res.capturedSeries.map((cs) => ({
      name: cs.name,
      labels: cs.labels,
      sample_count: cs.sampleCount,
    })),
    captured_log_lines: res.capturedLogLines.map((l) => ({ labels: l.labels, line: l.line })),
    component_health: res.componentHealth.map((h) => ({
      node_id: h.nodeId,
      node_label: h.nodeLabel,
      component: h.component,
      health_state: h.healthState,
      message: h.message,
    })),
    gate_diagnostics: res.gateDiagnostics,
    stderr_tail: res.stderrTail,
    error_code: res.errorCode,
    error_message: res.errorMessage,
    fidelity_note: res.fidelityNote,
  };
}

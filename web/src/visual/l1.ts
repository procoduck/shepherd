import { portHandleId } from './schemaAdapter';
import type { GraphDocument, GraphEdge, L1Diagnostic, SchemaPayload } from './types';

export function sanitizeLabel(label: string): string {
  return label
    .replace(/[^a-z0-9_]/gi, '_')
    .replace(/^[^a-z_]/i, '_$&')
    .toLowerCase();
}

export function validateGraph(
  doc: GraphDocument,
  schema: SchemaPayload,
  opts: { allowExperimental?: boolean } = {},
): L1Diagnostic[] {
  const diags: L1Diagnostic[] = [];
  const nodeById = new Map(doc.nodes.map((n) => [n.id, n]));
  const compDef = (name: string) => schema.components[name];
  const labels = new Map<string, string[]>();
  for (const n of doc.nodes) {
    const key = `${n.component}:${sanitizeLabel(n.label)}`;
    labels.set(key, [...(labels.get(key) ?? []), n.id]);
  }
  for (const ids of labels.values())
    if (ids.length > 1)
      for (const id of ids) {
        diags.push({
          layer: 'L1',
          severity: 'error',
          code: 'label_collision',
          node_id: id,
          message: `Duplicate label on ${nodeById.get(id)?.component ?? id}`,
        });
      }
  if (!opts.allowExperimental)
    for (const n of doc.nodes)
      if (compDef(n.component)?.stability === 'experimental') {
        diags.push({
          layer: 'L1',
          severity: 'error',
          code: 'experimental_gated',
          node_id: n.id,
          message: `${n.component} is experimental and requires the org toggle to be enabled`,
        });
      }
  for (const n of doc.nodes)
    for (const attr of compDef(n.component)?.attributes ?? []) {
      if (
        attr.type === 'secret' &&
        typeof n.props[attr.name] === 'string' &&
        n.props[attr.name] !== ''
      ) {
        diags.push({
          layer: 'L1',
          severity: 'error',
          code: 'secret_by_value',
          node_id: n.id,
          message: `${attr.name} is secret-typed — use a config node binding, not a literal value`,
        });
      }
      if (!attr.required || attr.type === 'secret' || attr.input_type) continue;
      const value = n.props[attr.name];
      if (value === undefined || value === null || value === '') {
        diags.push({
          layer: 'L1',
          severity: 'error',
          code: 'required_attr_missing',
          node_id: n.id,
          message: `${n.component} "${n.label}" is missing required attribute "${attr.name}"`,
        });
      }
    }
  const activeNodeIds = new Set(doc.nodes.filter((n) => !n.disabled).map((n) => n.id));
  const incoming = new Map<string, GraphEdge[]>();
  const outgoing = new Map<string, GraphEdge[]>();
  for (const e of doc.edges) {
    if (!activeNodeIds.has(e.from.node) || !activeNodeIds.has(e.to.node)) continue;
    const fromNode = nodeById.get(e.from.node);
    const toNode = nodeById.get(e.to.node);
    const fromDef = fromNode && compDef(fromNode.component);
    const toDef = toNode && compDef(toNode.component);
    if (fromNode && toNode) {
      const fromOutput = fromDef?.outputs?.find((o, i) => portHandleId(o, i) === e.from.port);
      const toInput = toDef?.inputs?.find((inp, i) => portHandleId(inp, i) === e.to.port);
      if (fromOutput && toInput && !portsCompatible(fromOutput.type, toInput.type)) {
        diags.push({
          layer: 'L1',
          severity: 'error',
          code: 'type_mismatch',
          node_id: toNode.id,
          message: `Wire type mismatch: ${fromOutput.type} → ${toInput.type}`,
        });
      }
    }
    incoming.set(e.to.node, [...(incoming.get(e.to.node) ?? []), e]);
    outgoing.set(e.from.node, [...(outgoing.get(e.from.node) ?? []), e]);
  }
  const adj = new Map(doc.nodes.map((n) => [n.id, new Set<string>()]));
  for (const e of doc.edges) {
    if (activeNodeIds.has(e.from.node) && activeNodeIds.has(e.to.node)) {
      adj.get(e.from.node)?.add(e.to.node);
    }
  }
  if (
    detectCycle(
      doc.nodes.map((n) => n.id),
      adj,
    )
  )
    diags.push({
      layer: 'L1',
      severity: 'error',
      code: 'cycle',
      message: 'Graph contains a cycle — Alloy graphs must be acyclic',
    });
  for (const n of doc.nodes) {
    if (n.disabled) continue;
    const def = compDef(n.component);
    if (!def) continue;
    for (const [idx, inp] of (def.inputs ?? []).entries()) {
      const prop = portHandleId(inp, idx);
      const wired = (incoming.get(n.id) ?? []).filter((e) => {
        if (e.to.port !== prop) return false;
        const source = nodeById.get(e.from.node);
        const sourceDef = source && compDef(source.component);
        return (
          !sourceDef ||
          (sourceDef.outputs?.some((o, i) => portHandleId(o, i) === e.from.port) ?? true)
        );
      });
      if (wired.length === 0 && inp.cardinality !== undefined) {
        diags.push({
          layer: 'L1',
          severity: 'error',
          code: 'dangling_input',
          node_id: n.id,
          message: `${n.component} "${n.label}" has no wires on input "${prop}"`,
        });
      }
      if (inp.cardinality !== 'list' && wired.length > 1) {
        diags.push({
          layer: 'L1',
          severity: 'warning',
          code: 'scalar_input_multi_wire',
          node_id: n.id,
          message: `${n.component} "${n.label}" input "${prop}" is scalar but has ${wired.length} wires`,
        });
      }
    }
    if (!(def.terminal_ok ?? false))
      for (const [idx, out] of (def.outputs ?? []).entries()) {
        const exportId = portHandleId(out, idx);
        if (!(outgoing.get(n.id) ?? []).some((e) => e.from.port === exportId)) {
          diags.push({
            layer: 'L1',
            severity: 'warning',
            code: 'output_nowhere',
            node_id: n.id,
            message: `${n.component} "${n.label}" output "${exportId}" goes nowhere`,
          });
        }
      }
  }
  if (
    doc.nodes.length > 0 &&
    !doc.nodes.some((n) => compDef(n.component)?.category === 'destinations' && !n.disabled)
  )
    diags.push({
      layer: 'L1',
      severity: 'error',
      code: 'no_destination',
      message: 'Pipeline has no Destination nodes — data goes nowhere',
    });
  return diags;
}

function detectCycle(ids: string[], adj: Map<string, Set<string>>): boolean {
  const color = new Map(ids.map((id) => [id, 0]));
  function dfs(u: string): boolean {
    color.set(u, 1);
    for (const v of adj.get(u) ?? []) {
      if (color.get(v) === 1) return true;
      if (color.get(v) === 0 && dfs(v)) return true;
    }
    color.set(u, 2);
    return false;
  }
  return ids.some((id) => color.get(id) === 0 && dfs(id));
}

export function portsCompatible(fromType: string, toType: string): boolean {
  if (fromType === toType) return true;
  return (
    (fromType === 'otel.any' || toType === 'otel.any') &&
    fromType.startsWith('otel') &&
    toType.startsWith('otel')
  );
}

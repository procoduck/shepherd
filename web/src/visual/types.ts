export type WireType =
  | 'targets'
  | 'prom.metrics'
  | 'loki.logs'
  | 'otel.traces'
  | 'otel.metrics'
  | 'otel.logs'
  | 'otel.any'
  | 'pyroscope.profiles';

export interface GraphNode {
  id: string;
  component: string;
  label: string;
  position: { x: number; y: number };
  props: Record<string, unknown>;
  disabled: boolean;
  notes: string;
  /**
   * The author's ordering of this node's top-level blocks, by block name.
   *
   * `props` is a plain object keyed by block name, so without this the order a
   * user arranged differently-named blocks in is never stored — not merely lost
   * when rendering. Alloy block order is semantic: loki.process runs stages in
   * document order, so `stage.json` before `stage.drop` extracts a field and
   * then drops on it, while the reverse drops on an empty map and silently
   * matches nothing.
   *
   * Absent means "no recorded order" and the renderer uses the schema's own
   * declaration order — what every graph saved before this field existed gets.
   */
  block_order?: string[];
}

export interface GraphEdge {
  id: string;
  from: { node: string; port: string };
  to: { node: string; port: string };
  order?: number;
}

export interface GraphBinding {
  node: string;
  prop: string;
  ref: { node: string; export: string; expr: string };
}
export interface GraphDocument {
  kind: 'alloy-graph/v1';
  schema_version: string;
  nodes: GraphNode[];
  edges: GraphEdge[];
  bindings: GraphBinding[];
  viewport: { x: number; y: number; zoom: number };
  meta: { created_with: string };
}
export interface AttrDef {
  name: string;
  type: string;
  required: boolean;
  values?: string[];
  input_type?: string;
}
export interface PortDef {
  prop?: string;
  export?: string;
  type: WireType;
  cardinality?: 'list' | 'scalar';
}
export interface ComponentDef {
  stability: 'ga' | 'public-preview' | 'experimental';
  doc: string;
  attributes: AttrDef[];
  blocks: BlockDef[];
  inputs: PortDef[];
  outputs: PortDef[];
  default_snippet: string;
  opaque?: boolean;
  category?: string;
  icon?: string;
  terminal_ok?: boolean;
  key_props?: string[];
}
export interface BlockDef {
  name: string;
  repeatable?: boolean;
  attributes?: AttrDef[];
  blocks?: BlockDef[];
}
export interface WireTypeDef {
  color: string;
  label: string;
}
export interface SchemaPayload {
  _meta: { alloy_version: string; components_total: number };
  components: Record<string, ComponentDef>;
  wire_types: Record<string, WireTypeDef>;
}
export interface L1Diagnostic {
  layer: 'L1';
  severity: 'error' | 'warning';
  code: string;
  node_id?: string;
  message: string;
}

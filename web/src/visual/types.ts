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
  /**
   * Read-only graph view only: the id of the module group (component
   * `declare`) this node sits in, absent at the top level. GraphView sets it
   * when it expands a `declare`; the builder never does, and the server's
   * Render refuses a graph that carries one.
   */
  parent?: string;
}

export interface GraphEdge {
  id: string;
  from: { node: string; port: string };
  to: { node: string; port: string };
  /**
   * Fan-in order among every OTHER edge landing on the same (to.node,
   * to.port) — what decides `forward_to = [a.receiver, b.receiver]`'s
   * sequence when a list-cardinality accepts port has more than one wire.
   * store.ts's `addEdge` stamps it (insertion order, so a freshly authored
   * graph renders identically to before this field existed) and `moveEdge`
   * is the inspector's reorder control (W5-08). Both renderers already sort
   * by it, falling back to array/insertion order when absent — a document
   * saved before this field was stamped, or one hand-authored without it,
   * still renders exactly as it always did.
   */
  order?: number;
}

/**
 * A config-node binding (design §3.4): the expression `ref.expr` supplies the
 * attribute `prop` names. `prop` is a top-level attribute name — rendered as
 * `${prop} = ${ref.expr}` after the node's body, as it always was — or a
 * nested binding path into the component's blocks (#426, `bindingPath.ts`):
 * `endpoint[0].basic_auth.password`, `client.tls.ca_pem`,
 * `stage.tenant[0].value`, rendered inside that block instance. The inspector's
 * binding picker writes these (`store.ts`'s `setBinding`); see `bindings.ts`
 * for the `{"$expr": ...}`-in-props channel that renders the same way.
 */
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
  /** Present when this component is a config/secret source the simulator
   *  resolves specially (S3's overlay field) — `local.file`, `remote.http`,
   *  `remote.s3`, `remote.vault`, `remote.kubernetes.secret`,
   *  `remote.kubernetes.configmap`. `bindings.ts`'s `secretSourceNodes` reads
   *  it to populate the inspector's binding picker (W5-01/W5-02). */
  sim_secret_source?: { mode: string };
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

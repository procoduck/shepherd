import deepEqual from 'fast-deep-equal';
import { nanoid } from 'nanoid';
import { temporal } from 'zundo';
import { create } from 'zustand';
import { validateGraph } from './l1';
import type { GraphDocument, GraphEdge, GraphNode, L1Diagnostic, SchemaPayload } from './types';

interface VisualStore {
  doc: GraphDocument;
  selected: string[];
  diagnostics: L1Diagnostic[];
  schema: SchemaPayload | null;
  allowExperimental: boolean;
  flowCheckActive: boolean;

  setSchema: (s: SchemaPayload) => void;
  addNode: (component: string, position: { x: number; y: number }) => void;
  /** Paste-specific variant: caller supplies the id and label. */
  addNodeWithId: (
    id: string,
    component: string,
    position: { x: number; y: number },
    label: string,
  ) => void;
  updateNode: (id: string, patch: Partial<GraphNode>) => void;
  removeNode: (id: string) => void;
  addEdge: (from: { node: string; port: string }, to: { node: string; port: string }) => void;
  pasteNodesAndEdges: (nodes: GraphNode[], edges: GraphEdge[]) => void;
  importGraph: (doc: GraphDocument) => void;
  resetDoc: () => void;
  removeEdge: (id: string) => void;
  updateViewport: (vp: { x: number; y: number; zoom: number }) => void;
  /** Idempotent — no-op when ids array is deeply equal to current. */
  setSelected: (ids: string[]) => void;
  setLabel: (id: string, label: string) => void;
  setDisabled: (id: string, disabled: boolean) => void;
  toggleFlowCheck: () => void;
}

function makeDefaultDoc(schemaVersion = 'alloy-v1.18.1'): GraphDocument {
  return {
    kind: 'alloy-graph/v1',
    schema_version: schemaVersion,
    nodes: [],
    edges: [],
    bindings: [],
    viewport: { x: 0, y: 0, zoom: 1 },
    meta: { created_with: 'shepherd-vb/1.0' },
  };
}

function revalidate(
  state: Pick<VisualStore, 'doc' | 'schema' | 'allowExperimental'>,
): L1Diagnostic[] {
  return state.schema
    ? validateGraph(state.doc, state.schema, { allowExperimental: state.allowExperimental })
    : [];
}

export const useVisualStore = create<VisualStore>()(
  temporal(
    (set, get) => ({
      doc: makeDefaultDoc(),
      selected: [],
      diagnostics: [],
      schema: null,
      allowExperimental: false,
      flowCheckActive: false,

      setSchema: (schema) => set({ schema, diagnostics: revalidate({ ...get(), schema }) }),

      addNode: (component, position) =>
        set((state) => {
          const node: GraphNode = {
            id: `n_${nanoid(8)}`,
            component,
            label: component.split('.').pop() ?? component,
            position,
            props: {},
            disabled: false,
            notes: '',
          };
          const doc = { ...state.doc, nodes: [...state.doc.nodes, node] };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      addNodeWithId: (id, component, position, label) =>
        set((state) => {
          const node: GraphNode = {
            id,
            component,
            label,
            position,
            props: {},
            disabled: false,
            notes: '',
          };
          const doc = { ...state.doc, nodes: [...state.doc.nodes, node] };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      updateNode: (id, patch) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) => (n.id === id ? { ...n, ...patch } : n)),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      removeNode: (id) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.filter((n) => n.id !== id),
            edges: state.doc.edges.filter((e) => e.from.node !== id && e.to.node !== id),
          };
          return {
            doc,
            diagnostics: revalidate({ ...state, doc }),
            selected: state.selected.filter((x) => x !== id),
          };
        }),

      addEdge: (from, to) =>
        set((state) => {
          if (
            from.node === to.node ||
            state.doc.edges.some(
              (e) =>
                e.from.node === from.node &&
                e.from.port === from.port &&
                e.to.node === to.node &&
                e.to.port === to.port,
            )
          )
            return state;
          const adj = new Map(state.doc.nodes.map((n) => [n.id, [] as string[]]));
          for (const e of state.doc.edges) adj.get(e.from.node)?.push(e.to.node);
          adj.get(from.node)?.push(to.node);
          if (
            hasCycle(
              state.doc.nodes.map((n) => n.id),
              adj,
            )
          )
            return state;
          const doc = {
            ...state.doc,
            edges: [...state.doc.edges, { id: `e_${nanoid(8)}`, from, to }],
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      pasteNodesAndEdges: (nodes, edges) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: [...state.doc.nodes, ...nodes],
            edges: [...state.doc.edges, ...edges],
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      importGraph: (doc) => set((state) => ({ doc, diagnostics: revalidate({ ...state, doc }) })),

      resetDoc: () => set({ doc: makeDefaultDoc(), selected: [], diagnostics: [] }),

      removeEdge: (id) =>
        set((state) => {
          const doc = { ...state.doc, edges: state.doc.edges.filter((e) => e.id !== id) };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      updateViewport: (viewport) => set((state) => ({ doc: { ...state.doc, viewport } })),

      // Idempotent: skip the store update when the selection hasn't changed.
      setSelected: (ids) =>
        set((state) => (deepEqual(state.selected, ids) ? state : { selected: ids })),

      setLabel: (id, label) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) => (n.id === id ? { ...n, label } : n)),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),

      toggleFlowCheck: () => set((state) => ({ flowCheckActive: !state.flowCheckActive })),

      setDisabled: (id, disabled) =>
        set((state) => {
          const doc = {
            ...state.doc,
            nodes: state.doc.nodes.map((n) => (n.id === id ? { ...n, disabled } : n)),
          };
          return { doc, diagnostics: revalidate({ ...state, doc }) };
        }),
    }),
    {
      partialize: (state) => ({ doc: state.doc }),
      limit: 100,
      equality: (a, b) => {
        const da = (a as { doc?: GraphDocument }).doc;
        const db = (b as { doc?: GraphDocument }).doc;
        if (!da || !db) return deepEqual(a, b);
        return deepEqual(
          { nodes: da.nodes, edges: da.edges, bindings: da.bindings },
          { nodes: db.nodes, edges: db.edges, bindings: db.bindings },
        );
      },
    },
  ),
);

function hasCycle(nodeIds: string[], adj: Map<string, string[]>): boolean {
  const color = new Map(nodeIds.map((id) => [id, 0]));
  function dfs(u: string): boolean {
    color.set(u, 1);
    for (const v of adj.get(u) ?? []) {
      if (color.get(v) === 1) return true;
      if (color.get(v) === 0 && dfs(v)) return true;
    }
    color.set(u, 2);
    return false;
  }
  return nodeIds.some((id) => color.get(id) === 0 && dfs(id));
}

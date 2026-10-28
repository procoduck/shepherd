import {
  type FocusEvent,
  type KeyboardEvent,
  type RefObject,
  useCallback,
  useRef,
  useState,
} from 'react';
import {
  checkConnection,
  type Direction,
  edgeAriaLabel,
  edgesOnHandle,
  type HandleRef,
  readingOrder,
  spatialNeighbor,
} from '../canvasA11y';
import { portHandleId } from '../schemaAdapter';
import { useVisualStore } from '../store';
import { rfEndpointsForEdge } from '../wireOrient';

/**
 * useCanvasKeyboard — the canvas's keyboard model (#426). Everything the mouse
 * does on the canvas has a key here; `CANVAS_SHORTCUTS` (canvasA11y.ts) is the
 * sheet `?` opens, and the docs list the same table.
 *
 * Focus lives on React Flow's own elements: each node (`.react-flow__node`,
 * focusable, named by `nodeAriaLabel`), each wire (`.react-flow__edge`), and
 * each port (`PortHandle`, focusable only programmatically — Enter on a node
 * goes into its ports). The handler runs in the CAPTURE phase on the canvas
 * wrapper and stops the keys it handles, so React Flow's own node keyboard
 * handling (arrows move a selected node, Enter selects) never sees them: one
 * model, not two competing ones.
 *
 * Every change goes through the store's ordinary actions — addEdge, removeEdge,
 * removeNode, removeSelected, updateNode — so a keyboard edit is one undo step
 * exactly like the mouse edit it stands for, and the read-only flag gates it
 * the same way. Each outcome is announced through a polite live region.
 */

/** The grid the canvas snaps a dragged node to (CanvasPane's snapGrid). */
const NUDGE = 8;

const ARROWS: Record<string, Direction> = {
  ArrowLeft: 'left',
  ArrowRight: 'right',
  ArrowUp: 'up',
  ArrowDown: 'down',
};

interface Wiring {
  from: HandleRef;
  fromName: string;
  candidates: HandleRef[];
}

const handleSelector = (h: HandleRef) =>
  `.react-flow__handle.${h.rfType}[data-nodeid="${CSS.escape(h.nodeId)}"][data-handleid="${CSS.escape(h.handleId)}"]`;

function refOf(el: Element): HandleRef | null {
  const nodeId = el.getAttribute('data-nodeid');
  const handleId = el.getAttribute('data-handleid');
  if (!nodeId || !handleId) return null;
  return { nodeId, handleId, rfType: el.classList.contains('source') ? 'source' : 'target' };
}

const isTyping = (el: HTMLElement) =>
  el.tagName === 'INPUT' ||
  el.tagName === 'TEXTAREA' ||
  el.tagName === 'SELECT' ||
  el.isContentEditable;

export function useCanvasKeyboard(
  wrapperRef: RefObject<HTMLDivElement | null>,
  syncSetSelected: (ids: string[]) => void,
) {
  const [announcement, setAnnouncement] = useState({ text: '', seq: 0 });
  const [helpOpen, setHelpOpen] = useState(false);
  const wiringRef = useRef<Wiring | null>(null);

  const announce = useCallback(
    (text: string) => setAnnouncement((a) => ({ text, seq: a.seq + 1 })),
    [],
  );

  const nodeEl = useCallback(
    (id: string) =>
      wrapperRef.current?.querySelector<HTMLElement>(
        `.react-flow__node[data-id="${CSS.escape(id)}"]`,
      ) ?? null,
    [wrapperRef],
  );
  const focusNode = useCallback(
    (id: string | null | undefined) => {
      if (id) nodeEl(id)?.focus();
    },
    [nodeEl],
  );
  const focusHandle = useCallback(
    (h: HandleRef) => wrapperRef.current?.querySelector<HTMLElement>(handleSelector(h))?.focus(),
    [wrapperRef],
  );
  const nameOf = (id: string) => {
    const n = useVisualStore.getState().doc.nodes.find((x) => x.id === id);
    return n ? `${n.component} "${n.label}"` : id;
  };

  const endWiring = useCallback(() => {
    wiringRef.current = null;
    useVisualStore.getState().setConnectingFrom(null);
  }, []);

  const startWiring = useCallback(
    (from: HandleRef) => {
      const { schema, doc, readOnly, setConnectingFrom } = useVisualStore.getState();
      if (readOnly) {
        announce('This pipeline is read-only.');
        return;
      }
      const all = Array.from(wrapperRef.current?.querySelectorAll('.react-flow__handle') ?? []);
      const candidates = all
        .map(refOf)
        .filter((h): h is HandleRef => h !== null && checkConnection(schema, doc, from, h).ok);
      const fromName = `${from.handleId} on ${nameOf(from.nodeId)}`;
      if (candidates.length === 0) {
        announce(`No compatible port for ${fromName}.`);
        return;
      }
      const node = doc.nodes.find((n) => n.id === from.nodeId);
      const def = node && schema?.components[node.component];
      const ports = from.rfType === 'source' ? def?.outputs : def?.inputs;
      setConnectingFrom({
        nodeId: from.nodeId,
        handleId: from.handleId,
        handleType: from.rfType,
        wireType: ports?.find((p, i) => portHandleId(p, i) === from.handleId)?.type ?? null,
      });
      wiringRef.current = { from, fromName, candidates };
      focusHandle(candidates[0]);
      announce(
        `Wiring from ${fromName}. ${candidates.length} compatible ${candidates.length === 1 ? 'port' : 'ports'}: arrow keys move between them, Enter connects, Escape cancels.`,
      );
    },
    [announce, focusHandle, wrapperRef],
  );

  const connectTo = useCallback(
    (to: HandleRef) => {
      const wiring = wiringRef.current;
      if (!wiring) return;
      const { schema, doc, addEdge } = useVisualStore.getState();
      const outcome = checkConnection(schema, doc, wiring.from, to);
      if (!outcome.ok) {
        announce('That port is not compatible; choose another or press Escape.');
        return;
      }
      const { replaced } = addEdge(outcome.from, outcome.to);
      endWiring();
      announce(
        `Connected ${wiring.fromName} to ${to.handleId} on ${nameOf(to.nodeId)}${replaced.length > 0 ? ', replacing the wire that was there' : ''}.`,
      );
    },
    [announce, endWiring],
  );

  const onNodeKey = (e: KeyboardEvent, el: HTMLElement): boolean => {
    const id = el.getAttribute('data-id');
    if (!id) return false;
    const state = useVisualStore.getState();
    const dir = ARROWS[e.key];
    if (dir && !e.shiftKey) {
      const next = spatialNeighbor(state.doc.nodes, id, dir);
      if (next) focusNode(next);
      else
        announce(
          `No node ${dir === 'left' || dir === 'right' ? `to the ${dir}` : dir === 'up' ? 'above' : 'below'}.`,
        );
      return true;
    }
    if (dir && e.shiftKey) {
      if (state.readOnly) {
        announce('This pipeline is read-only.');
        return true;
      }
      const node = state.doc.nodes.find((n) => n.id === id);
      if (!node) return true;
      const dx = dir === 'left' ? -NUDGE : dir === 'right' ? NUDGE : 0;
      const dy = dir === 'up' ? -NUDGE : dir === 'down' ? NUDGE : 0;
      const position = { x: node.position.x + dx, y: node.position.y + dy };
      state.updateNode(id, { position });
      announce(`Moved to ${position.x}, ${position.y}.`);
      return true;
    }
    switch (e.key) {
      case 'Home':
      case 'End': {
        const order = readingOrder(state.doc.nodes);
        focusNode((e.key === 'Home' ? order[0] : order.at(-1))?.id);
        return true;
      }
      case 'Enter': {
        const first = el.querySelector<HTMLElement>('.react-flow__handle');
        if (first) first.focus();
        else announce('This node has no ports.');
        return true;
      }
      case ' ':
        syncSetSelected([id]);
        announce(`Selected ${nameOf(id)}.`);
        return true;
      case 'i':
      case 'I': {
        syncSetSelected([id]);
        // After the inspector has rendered the newly selected node.
        requestAnimationFrame(() => {
          const field = document.querySelector<HTMLElement>(
            '[data-testid="inspector"] input, [data-testid="inspector"] select, [data-testid="inspector"] textarea',
          );
          if (field) field.focus();
          else announce('Open the Properties panel to edit this node.');
        });
        return true;
      }
      case 'Escape':
        syncSetSelected([]);
        return true;
      case 'Delete':
      case 'Backspace': {
        if (state.readOnly) {
          announce('This pipeline is read-only.');
          return true;
        }
        const wires = state.doc.edges.filter((x) => x.from.node === id || x.to.node === id).length;
        const name = nameOf(id);
        const next =
          spatialNeighbor(state.doc.nodes, id, 'right') ??
          spatialNeighbor(state.doc.nodes, id, 'left');
        state.removeNode(id);
        announce(
          `Deleted ${name}${wires > 0 ? ` and ${wires} ${wires === 1 ? 'wire' : 'wires'}` : ''}.`,
        );
        if (next) requestAnimationFrame(() => focusNode(next));
        else wrapperRef.current?.focus();
        return true;
      }
    }
    return false;
  };

  const onPortKey = (e: KeyboardEvent, el: HTMLElement): boolean => {
    const here = refOf(el);
    if (!here) return false;
    const wiring = wiringRef.current;
    const dir = ARROWS[e.key];
    if (wiring) {
      if (dir) {
        const list = wiring.candidates;
        const at = list.findIndex(
          (h) =>
            h.nodeId === here.nodeId && h.handleId === here.handleId && h.rfType === here.rfType,
        );
        const step = dir === 'left' || dir === 'up' ? -1 : 1;
        focusHandle(list[(at + step + list.length) % list.length]);
        return true;
      }
      if (e.key === 'Enter' || e.key === ' ') {
        connectTo(here);
        return true;
      }
      if (e.key === 'Escape') {
        const back = wiring.from;
        endWiring();
        focusHandle(back);
        announce('Wiring cancelled.');
        return true;
      }
      if (e.key === 'Tab') {
        endWiring();
        announce('Wiring cancelled.');
        return false;
      }
      return false;
    }
    const nodeElement = el.closest<HTMLElement>('.react-flow__node');
    if (dir && nodeElement) {
      const ports = Array.from(nodeElement.querySelectorAll<HTMLElement>('.react-flow__handle'));
      const at = ports.indexOf(el);
      const step = dir === 'left' || dir === 'up' ? -1 : 1;
      ports[(at + step + ports.length) % ports.length]?.focus();
      return true;
    }
    switch (e.key) {
      case 'Enter':
      case ' ':
        startWiring(here);
        return true;
      case 'Escape':
        nodeElement?.focus();
        return true;
      case 'Delete':
      case 'Backspace': {
        const state = useVisualStore.getState();
        if (state.readOnly) {
          announce('This pipeline is read-only.');
          return true;
        }
        const wires = edgesOnHandle(
          state.doc.edges,
          (x) => rfEndpointsForEdge(state.schema, state.doc, x),
          here,
        );
        if (wires.length === 0) {
          announce(`No wires on ${here.handleId}.`);
          return true;
        }
        // One undo step for all of them, as a box-selected delete would be.
        syncSetSelected(wires.map((w) => w.id));
        state.removeSelected();
        syncSetSelected([here.nodeId]);
        announce(
          `Deleted ${wires.length} ${wires.length === 1 ? 'wire' : 'wires'} on ${here.handleId}.`,
        );
        return true;
      }
    }
    return false;
  };

  const onEdgeKey = (e: KeyboardEvent, el: HTMLElement): boolean => {
    const id = el.getAttribute('data-id');
    const state = useVisualStore.getState();
    const edge = state.doc.edges.find((x) => x.id === id);
    if (!edge) return false;
    switch (e.key) {
      case 'Delete':
      case 'Backspace': {
        if (state.readOnly) {
          announce('This pipeline is read-only.');
          return true;
        }
        const name = edgeAriaLabel(edge, state.doc.nodes);
        state.removeEdge(edge.id);
        announce(`Deleted ${name.charAt(0).toLowerCase()}${name.slice(1)}.`);
        focusNode(edge.from.node);
        return true;
      }
      case 'Enter':
      case ' ':
        syncSetSelected([edge.id]);
        announce('Wire selected.');
        return true;
      case 'Escape':
        focusNode(edge.from.node);
        return true;
    }
    return false;
  };

  const onKeyDownCapture = (e: KeyboardEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement;
    if (isTyping(target) || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === '?') {
      e.preventDefault();
      e.stopPropagation();
      setHelpOpen(true);
      return;
    }
    let handled = false;
    if (target.classList.contains('react-flow__handle')) handled = onPortKey(e, target);
    else if (target.classList.contains('react-flow__node')) handled = onNodeKey(e, target);
    else if (target.classList.contains('react-flow__edge')) handled = onEdgeKey(e, target);
    else if (
      target === wrapperRef.current &&
      (ARROWS[e.key] || e.key === 'Home' || e.key === 'End' || e.key === 'Enter')
    ) {
      const order = readingOrder(useVisualStore.getState().doc.nodes);
      const first = e.key === 'End' ? order.at(-1) : order[0];
      if (first) focusNode(first.id);
      else announce('The canvas is empty. Add a component from the palette.');
      handled = true;
    }
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  // Focus does NOT select (Space and I do). Selection-follows-focus was built
  // first and fed a loop: each keyboard step wrote the store's selection,
  // React Flow's onSelectionChange echoed the previous one back a commit
  // later, and a few quick arrow presses ping-ponged the two until React's
  // update-depth guard tore the page down. Keeping selection explicit also
  // leaves a mouse multi-selection alone while the keyboard moves around.

  // Focus leaving every port ends an unfinished keyboard wire.
  const onBlurCapture = (e: FocusEvent<HTMLDivElement>) => {
    if (!wiringRef.current) return;
    const next = e.relatedTarget as HTMLElement | null;
    if (!next?.classList.contains('react-flow__handle')) {
      endWiring();
      announce('Wiring cancelled.');
    }
  };

  return {
    announcement,
    helpOpen,
    setHelpOpen,
    handlers: { onKeyDownCapture, onBlurCapture },
  };
}

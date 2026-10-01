import { createContext, useContext } from 'react';

/**
 * #226: whether the inspector shows the selected node's values for READING
 * only (a viewer). Provided once by InspectorPanel rather than threaded through
 * every widget's props, so a widget added later inherits it without having to
 * remember to. A widget reading `true` keeps its value visible and copyable —
 * text-like inputs go `readOnly` (still selectable), controls with no
 * read-only state (checkbox, select) go `disabled` — and renders no
 * affordance that adds, removes, binds or reorders anything.
 */
export const InspectorReadOnlyContext = createContext(false);

export function useInspectorReadOnly(): boolean {
  return useContext(InspectorReadOnlyContext);
}

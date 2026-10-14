import { Controls, type FitViewOptions, MiniMap } from '@xyflow/react';
import { useTheme } from './PipelineNode';

/**
 * The canvas chrome the visual builder (CanvasPane) and the read-only graph
 * view (GraphViewPage) share: zoom controls top-left, a small themed minimap
 * bottom-left, and the fit padding that keeps a fitted graph clear of both.
 * The graph view used React Flow's defaults — both panels in the bottom-left
 * corner, the minimap over the zoom buttons, and white in dark mode (2026-10-14
 * walkthrough M1, L3).
 */

// The minimap's box, in screen px. React Flow's default is 200x150; at that
// size, in the bottom-left corner, it covered the first node of a fitted
// graph (#251).
export const MINIMAP_W = 160;
export const MINIMAP_H = 100;

// How every fit frames the graph. Per side, in screen px, so a fitted graph
// clears the canvas's own overlays: the bottom band the minimap sits in, and
// the zoom controls' column at the top left. A uniform fraction (the old
// `padding: 0.15`) let the fit centre a node straight under the minimap (#251).
//
// bottom = MINIMAP_H + the 15px React Flow insets every panel by
// (`.react-flow__panel`) + a 25px gap; left clears the 26px controls column.
export const FIT_PADDING = {
  top: '32px',
  right: '32px',
  bottom: '140px',
  left: '64px',
} as const;

/** Zoom in/out and fit, top-left: the minimap owns bottom-left, and stacking
 *  both hid the zoom buttons. */
export function CanvasControls({
  fitViewOptions,
  showInteractive = true,
}: {
  fitViewOptions: FitViewOptions;
  /** React Flow's lock toggle; the read-only graph view has nothing to lock. */
  showInteractive?: boolean;
}) {
  return (
    <Controls
      position='top-left'
      showInteractive={showInteractive}
      fitViewOptions={fitViewOptions}
      className='[&>button]:bg-card [&>button]:border-border [&>button]:fill-zinc-300 [&>button:hover]:bg-accent/20'
    />
  );
}

/**
 * Bottom-left, shrunk from React Flow's 200x150 default; FIT_PADDING keeps
 * every fit clear of it (#251).
 *
 * React Flow's minimap defaults to a light palette; pin it to the current
 * token layer explicitly (F2, 2026-09-14 walkthrough fixes: these were
 * dark-only literals, unreadable once light mode existed) — bgColor/nodeColor/
 * nodeStrokeColor mirror --color-panel/--color-border-strong/--color-accent
 * for each theme, and maskColor (the non-viewport overlay) mirrors bgColor so
 * it doesn't render as a heavy near-black slab over a light panel.
 */
export function CanvasMiniMap() {
  const theme = useTheme();
  return (
    <MiniMap
      position='bottom-left'
      pannable
      zoomable
      style={{ width: MINIMAP_W, height: MINIMAP_H }}
      bgColor={theme === 'light' ? '#f4f4f5' : '#0e0e11'}
      maskColor={theme === 'light' ? 'rgba(244,244,245,0.75)' : 'rgba(9,9,11,0.75)'}
      nodeColor={theme === 'light' ? '#d4d4d8' : '#3f3f46'}
      nodeStrokeColor={theme === 'light' ? '#4f46e5' : '#6366f1'}
      className='!border !border-border !rounded-md'
    />
  );
}

import { Handle, type HandleProps, useNodeConnections } from '@xyflow/react';
import type { CSSProperties } from 'react';
import { portAriaLabel } from '../canvasA11y';
import type { PortLayout } from './PipelineNode';

/**
 * One port's React Flow handle, made reachable from the keyboard (#426): it is
 * a button a screen reader names by port, direction, wire type and wire count
 * (`forward_to, output, prom.metrics, 1 wire`), focusable only
 * programmatically (`tabIndex={-1}`) — useCanvasKeyboard moves focus into a
 * node's ports on Enter, so the ports do not add a Tab stop each. The wire
 * count comes from React Flow's own connection lookup for this handle, so the
 * node re-renders for a wire on this port only.
 */
export function PortHandle({
  port,
  position,
  style,
  className,
}: {
  port: PortLayout;
  position: HandleProps['position'];
  style: CSSProperties;
  className: string;
}) {
  const wires = useNodeConnections({ handleType: port.rfType, handleId: port.handleId }).length;
  return (
    <Handle
      type={port.rfType}
      position={position}
      id={port.handleId}
      style={style}
      className={className}
      tabIndex={-1}
      role='button'
      aria-label={portAriaLabel(port, wires)}
      data-port-role={port.role}
    />
  );
}

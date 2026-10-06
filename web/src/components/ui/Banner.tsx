import type { ReactNode } from 'react';

/**
 * Shared inline notice (S4). A handful of pages hand-rolled the same
 * rounded/border/padding notice box for a fact the viewer needs before
 * acting (a group-backed team's membership note, a forbidden-action
 * explanation). `variant='error'` gets role='alert' -- the others are
 * informational, not something a screen reader should interrupt for.
 */
type BannerVariant = 'info' | 'warning' | 'error' | 'success';

const VARIANT_CLASSES: Record<BannerVariant, string> = {
  info: 'border-info/30 bg-info-surface text-info',
  warning: 'border-warn/30 bg-warn-surface text-warn',
  error: 'border-danger/30 bg-danger-surface text-danger',
  success: 'border-ok/30 bg-ok-surface text-ok',
};

export function Banner({
  variant = 'info',
  testId,
  className = '',
  children,
}: {
  variant?: BannerVariant;
  testId?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      role={variant === 'error' ? 'alert' : undefined}
      data-testid={testId}
      className={`rounded-md border p-2.5 text-xs ${VARIANT_CLASSES[variant]} ${className}`.trim()}
    >
      {children}
    </div>
  );
}

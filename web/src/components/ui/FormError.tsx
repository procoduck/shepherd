import type { ReactNode } from 'react';
import { Banner } from './Banner';

/**
 * A form's refusal, shown inside the form (#249). Refusals used to be toasts:
 * transient, away from the form, and gone before a long server reason could
 * be read. Renders nothing when there is no error. Banner's error variant
 * carries role="alert", so a screen reader announces it when it appears.
 */
export function FormError({
  message,
  testId = 'form-error',
  className,
}: {
  message: ReactNode | null | undefined;
  testId?: string;
  className?: string;
}) {
  if (!message) return null;
  return (
    <Banner variant='error' testId={testId} className={className}>
      {message}
    </Banner>
  );
}

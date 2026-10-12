/**
 * A control that is unavailable for a stated reason (2026-10-12 walkthrough).
 * A `disabled` button cannot take focus, so a reason kept in its title never
 * reached a keyboard or screen-reader user. `aria-disabled` keeps the button
 * in the tab order and announced as unavailable, and the reason becomes its
 * accessible description; the title stays for the mouse. The caller's click
 * handler must still refuse while `reason` is set.
 */
export function unavailableProps(reason: string | undefined, descriptionId: string) {
  return reason
    ? { 'aria-disabled': true as const, 'aria-describedby': descriptionId, title: reason }
    : {};
}

/** The visually hidden description `unavailableProps` points at. */
export function UnavailableReason({ id, reason }: { id: string; reason: string | undefined }) {
  return reason ? (
    <span id={id} className='sr-only'>
      {reason}
    </span>
  ) : null;
}

/** The disabled look, for `aria-disabled` as well as `disabled`. */
export const UNAVAILABLE_CLASS =
  'disabled:opacity-50 disabled:cursor-not-allowed aria-disabled:opacity-50 aria-disabled:cursor-not-allowed';

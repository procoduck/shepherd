import type {
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from 'react';

/**
 * Form field primitives (S4). The border/background/padding literal
 * `border-border-strong bg-card px-3 py-1.5 text-sm` was hand-copied onto
 * every <input>/<select> in the app; Input/Select/Textarea are that look,
 * defined once. Field owns the label + spacing + optional hint/error text
 * that used to be hand-copied around each one.
 *
 * Input/Select/Textarea deliberately do not include the `mt-1` gap between
 * label text and control -- Field supplies that via its wrapping div, so a
 * control used standalone (without Field, e.g. PipelineEditorPage's
 * sibling-label layout) doesn't get a spurious extra margin.
 */
// The disabled look (#309): a chart-managed SSO page's disabled fields used to
// read as editable ones that ignored typing.
const CONTROL_BASE =
  'w-full rounded-md border border-border-strong bg-card px-3 py-1.5 text-sm disabled:cursor-not-allowed disabled:opacity-60';

export function Field({
  label,
  optional,
  hint,
  error,
  className = '',
  children,
}: {
  label: ReactNode;
  optional?: boolean;
  hint?: ReactNode;
  error?: ReactNode;
  /** Extra classes appended to the label (e.g. `flex-1` inside a flex row). */
  className?: string;
  children: ReactNode;
}) {
  return (
    <label className={`block text-xs font-medium text-muted ${className}`.trim()}>
      {label}
      {optional && <span className='text-muted-3'> (optional)</span>}
      <div className='mt-1'>{children}</div>
      {hint && <span className='mt-1 block text-2xs font-normal text-muted-3'>{hint}</span>}
      {error && <span className='mt-1 block text-xs text-red-400'>{error}</span>}
    </label>
  );
}

export function Input({
  className = '',
  mono,
  ...props
}: InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }) {
  return (
    <input
      {...props}
      className={`${CONTROL_BASE} ${mono ? 'font-mono' : ''} ${className}`.trim()}
    />
  );
}

export function Select({ className = '', ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`${CONTROL_BASE} ${className}`.trim()} />;
}

export function Textarea({
  className = '',
  mono,
  ...props
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { mono?: boolean }) {
  return (
    <textarea
      {...props}
      className={`${CONTROL_BASE} ${mono ? 'font-mono' : ''} ${className}`.trim()}
    />
  );
}

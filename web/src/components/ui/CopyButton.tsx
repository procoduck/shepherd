import { Check, Copy } from 'lucide-react';
import { useState } from 'react';
import { toast } from 'sonner';

/** Copies text to the clipboard, confirming with a brief "Copied". */
export function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type='button'
      aria-label={`Copy ${label}`}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          toast.error('Copy failed — select and copy the text manually');
        }
      }}
      className='flex items-center gap-1 rounded border border-border px-2 py-1 text-xs text-muted hover:text-zinc-200'
    >
      {copied ? <Check size={12} /> : <Copy size={12} />} {copied ? 'Copied' : 'Copy'}
    </button>
  );
}

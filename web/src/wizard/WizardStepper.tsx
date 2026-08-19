import { Check } from 'lucide-react';

export interface WizardStepperItem {
  id: string;
  title: string;
}

interface WizardStepperProps {
  steps: WizardStepperItem[];
  activeIndex: number;
}

/**
 * Left vertical stepper rail (spec §13.5): numbered circles — active =
 * indigo filled, done = emerald check, upcoming = zinc outline — plus step
 * labels. Purely presentational; the caller owns navigation.
 */
export function WizardStepper({ steps, activeIndex }: WizardStepperProps) {
  return (
    <div className='w-56 shrink-0 space-y-1'>
      {steps.map((step, i) => {
        const done = i < activeIndex;
        const active = i === activeIndex;
        return (
          <div key={step.id} className='flex items-center gap-3 py-2'>
            <span
              className={
                'flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs font-medium ' +
                (active
                  ? 'bg-indigo-600 text-white'
                  : done
                    ? 'bg-emerald-500/20 text-emerald-400'
                    : 'border border-border-strong text-muted-2')
              }
              data-testid={`wizard-step-indicator-${i}`}
              data-state={active ? 'active' : done ? 'done' : 'upcoming'}
            >
              {done ? <Check size={13} /> : i + 1}
            </span>
            <span
              className={
                'text-xs font-medium ' +
                (active ? 'text-zinc-100' : done ? 'text-muted' : 'text-muted-2')
              }
            >
              {step.title}
            </span>
          </div>
        );
      })}
    </div>
  );
}

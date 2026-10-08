import { describe, expect, it } from 'vitest';
import { isWizardRevision, lastWizardRevision } from './wizardRevisions';

describe('isWizardRevision', () => {
  it('is the wizard commit and every re-render, on a wizard pipeline only', () => {
    expect(isWizardRevision('created', 'wizard')).toBe(true);
    expect(isWizardRevision('re-rendered: destination "mimir" updated', 'wizard')).toBe(true);
    expect(isWizardRevision('re-rendered: pre-#260 destination writer', 'wizard')).toBe(true);
    expect(isWizardRevision('updated', 'wizard')).toBe(false);
    expect(isWizardRevision('Restored from revision 1', 'wizard')).toBe(false);
    expect(isWizardRevision('detached from wizard', 'wizard')).toBe(false);
    // An editor-created pipeline's "created" is not a wizard's.
    expect(isWizardRevision('created', 'ui')).toBe(false);
  });
});

describe('lastWizardRevision', () => {
  it('is the newest wizard revision, whatever order the list is in', () => {
    const revisions = [
      { revision: 1, changeNote: 'created' },
      { revision: 4, changeNote: 'updated' },
      { revision: 3, changeNote: 're-rendered: destination "mimir" updated' },
      { revision: 2, changeNote: 'updated' },
    ];
    expect(lastWizardRevision(revisions, 'wizard')).toBe(3);
    expect(lastWizardRevision(revisions, 'ui')).toBeNull();
    expect(lastWizardRevision([], 'wizard')).toBeNull();
  });
});

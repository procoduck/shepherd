import { describe, expect, it } from 'vitest';
import { destinationUpdatedMessage } from './destinationUpdated';

describe('destinationUpdatedMessage (#309)', () => {
  it('says nothing extra when no pipeline was regenerated', () => {
    expect(destinationUpdatedMessage(0)).toBe('Destination updated');
  });

  it('counts one pipeline in the singular', () => {
    expect(destinationUpdatedMessage(1)).toBe('Destination updated — 1 pipeline regenerated');
  });

  it('counts several pipelines in the plural', () => {
    expect(destinationUpdatedMessage(3)).toBe('Destination updated — 3 pipelines regenerated');
  });
});

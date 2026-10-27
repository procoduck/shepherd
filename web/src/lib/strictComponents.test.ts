import { describe, expect, it } from 'vitest';
import { strictComponentClassification, strictComponentError } from './strictComponents';

describe('admin-allowed components', () => {
  it('accepts component names the server takes', () => {
    for (const v of ['otelcol.auth.google', 'loki.source.future', 'prometheus.exporter.x_2']) {
      expect(strictComponentError(v), v).toBe('');
    }
  });

  it('refuses what cannot be a component name', () => {
    expect(strictComponentError('')).toBe('Enter the component name');
    expect(strictComponentError(' loki.write')).toContain('spaces');
    for (const v of ['loki', 'Loki.write', 'loki..write', '1oki.write', 'loki.write.', 'a b.c']) {
      expect(strictComponentError(v), v).toContain('Use a component name');
    }
    expect(strictComponentError(`a.${'b'.repeat(130)}`)).toContain('Use a component name');
  });

  it('names the shipped classification', () => {
    expect(strictComponentClassification({ classification: 'refused' })).toBe('classified refused');
    expect(strictComponentClassification({ classification: '' })).toBe('unclassified');
  });
});

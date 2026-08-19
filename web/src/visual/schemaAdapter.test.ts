import { describe, expect, it } from 'vitest';
import { schemaFixture } from '../../tests/fixtures/schema-fixture';
import { getCategoryColor, getWireColor, portHandleId } from './schemaAdapter';

describe('portHandleId (A1)', () => {
  it('prefers `prop` when present', () => {
    expect(portHandleId({ prop: 'targets', export: 'other' }, 3)).toBe('targets');
  });
  it('falls back to `export` when `prop` is absent', () => {
    expect(portHandleId({ export: 'metrics' }, 2)).toBe('metrics');
  });
  it('falls back to a positional id when neither `prop` nor `export` is present', () => {
    expect(portHandleId({}, 0)).toBe('p0');
    expect(portHandleId({}, 4)).toBe('p4');
  });
});

describe('getWireColor / getCategoryColor (A4)', () => {
  it('reads the wire color from the schema payload when present', () => {
    expect(getWireColor(schemaFixture, 'loki.logs')).toBe('green');
  });
  it('falls back to the built-in hex table for a wire type missing from the schema payload', () => {
    // schemaFixture's wire_types omits prom.metrics' sibling colors we don't exercise elsewhere —
    // pyroscope.profiles is never in schemaFixture.wire_types, so this always exercises the fallback.
    expect(getWireColor(schemaFixture, 'pyroscope.profiles')).toBe('#f43f5e');
  });
  it('falls back to a default hex for a wire type in neither the schema nor the fallback table', () => {
    expect(getWireColor(schemaFixture, 'totally.unknown')).toBe('#94a3b8');
  });
  it('falls back to the built-in hex table entirely when schema is null', () => {
    expect(getWireColor(null, 'targets')).toBe('#8b5cf6');
  });
  it('falls back to the built-in category table — schemaFixture carries no categories section', () => {
    expect(getCategoryColor(schemaFixture, 'sources')).toBe('#3b82f6');
    expect(getCategoryColor(schemaFixture, 'destinations')).toBe('#10b981');
  });
  it('reads the category color from the schema payload when the overlay serves one (backend half of A4)', () => {
    const withCategories = {
      ...schemaFixture,
      categories: { sources: { color: '#123456', label: 'Sources' } },
    };
    expect(getCategoryColor(withCategories, 'sources')).toBe('#123456');
  });
});

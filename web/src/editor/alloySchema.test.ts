import { describe, expect, it } from 'vitest';
import { alloySchema, specRequiredComponents } from './alloySchema';

describe('alloySchema drift guard', () => {
  it('every component listed in spec §13.6 is present in alloySchema', () => {
    for (const component of specRequiredComponents) {
      const exists = Object.hasOwn(alloySchema, component);
      expect(
        exists,
        `Component "${component}" is required by spec §13.6 but missing from alloySchema`,
      ).toBe(true);
    }
  });

  it('every component has required fields', () => {
    for (const [name, def] of Object.entries(alloySchema)) {
      expect(typeof def.doc, `${name}.doc must be string`).toBe('string');
      expect(typeof def.hasLabel, `${name}.hasLabel must be boolean`).toBe('boolean');
      expect(Array.isArray(def.exports), `${name}.exports must be array`).toBe(true);
    }
  });
});

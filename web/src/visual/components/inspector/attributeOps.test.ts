import { describe, expect, it } from 'vitest';
import {
  addListItem,
  addMapRow,
  coerceScalar,
  formatDefault,
  formatScalar,
  isSet,
  listValue,
  mapFromRows,
  mapValue,
  removeListItem,
  removeMapRow,
  replaceListItem,
  setMapRow,
  widgetFor,
} from './attributeOps';

describe('widgetFor', () => {
  it('maps every schema type to its widget', () => {
    expect(widgetFor({ name: 'x', type: 'secret' })).toBe('secret');
    expect(widgetFor({ name: 'x', type: 'capsule' })).toBe('capsule');
    expect(widgetFor({ name: 'x', type: 'bool' })).toBe('bool');
    expect(widgetFor({ name: 'x', type: 'number' })).toBe('number');
    expect(widgetFor({ name: 'x', type: 'duration' })).toBe('duration');
    expect(widgetFor({ name: 'x', type: 'list' })).toBe('list');
    expect(widgetFor({ name: 'x', type: 'map' })).toBe('map');
    expect(widgetFor({ name: 'x', type: 'string' })).toBe('string');
  });

  it('gives a plain string an enum (select) widget only when the schema carries values', () => {
    expect(widgetFor({ name: 'action', type: 'string', values: ['keep', 'drop'] })).toBe('enum');
    expect(widgetFor({ name: 'action', type: 'string', values: [] })).toBe('string');
    expect(widgetFor({ name: 'action', type: 'string' })).toBe('string');
  });

  it('degrades an attribute with no declared type to a plain string widget (D4)', () => {
    expect(widgetFor({ name: 'mystery' })).toBe('string');
  });
});

describe('coerceScalar — F6: a value must be stored with its correct JSON type', () => {
  it('keeps a number a number, not the string the <input> produced', () => {
    const stored = coerceScalar('number', '5000');
    expect(stored).toBe(5000);
    expect(typeof stored).toBe('number');
  });

  it('round-trips a float and a negative number', () => {
    expect(coerceScalar('number', '3.5')).toBe(3.5);
    expect(coerceScalar('number', '-12')).toBe(-12);
  });

  it('keeps unparsable numeric text as text rather than silently dropping it', () => {
    expect(coerceScalar('number', 'not-a-number')).toBe('not-a-number');
  });

  it('clears the key on an empty box instead of storing an empty string', () => {
    expect(coerceScalar('number', '')).toBeUndefined();
    expect(coerceScalar('string', '')).toBeUndefined();
    expect(coerceScalar('duration', '')).toBeUndefined();
  });

  it('coerces a checkbox to a real boolean', () => {
    expect(coerceScalar('bool', true)).toBe(true);
    expect(coerceScalar('bool', false)).toBe(false);
    expect(typeof coerceScalar('bool', true)).toBe('boolean');
  });

  it('keeps string/duration/enum values as strings', () => {
    expect(coerceScalar('string', 'hello')).toBe('hello');
    expect(coerceScalar('duration', '30s')).toBe('30s');
  });
});

describe('formatScalar / coerceScalar round trip', () => {
  it('a number formatted back to text and re-coerced is the same number', () => {
    const original = 42;
    const asText = formatScalar(original);
    expect(asText).toBe('42');
    expect(coerceScalar('number', asText)).toBe(42);
  });

  it('an unset value formats to an empty box', () => {
    expect(formatScalar(undefined)).toBe('');
    expect(formatScalar(null)).toBe('');
  });
});

describe('isSet', () => {
  it('treats empty string/array/object as unset, and false/0 as set', () => {
    expect(isSet(undefined)).toBe(false);
    expect(isSet('')).toBe(false);
    expect(isSet('  ')).toBe(false);
    expect(isSet([])).toBe(false);
    expect(isSet({})).toBe(false);
    expect(isSet(false)).toBe(true);
    expect(isSet(0)).toBe(true);
    expect(isSet('x')).toBe(true);
  });
});

describe('list widget ops', () => {
  it('adds and removes chips, skipping a blank add', () => {
    let v: unknown = addListItem(undefined, 'a');
    v = addListItem(v, 'b');
    expect(listValue(v)).toEqual(['a', 'b']);
    v = addListItem(v, '   ');
    expect(listValue(v)).toEqual(['a', 'b']);
    v = removeListItem(v, 0);
    expect(listValue(v)).toEqual(['b']);
  });

  it('replaces an item in place', () => {
    const v = replaceListItem(['a', 'b', 'c'], 1, 'B');
    expect(v).toEqual(['a', 'B', 'c']);
  });

  it('coarsens non-string elements to their string form on display', () => {
    expect(listValue([1, 'two', true])).toEqual(['1', 'two', 'true']);
  });
});

describe('map widget ops', () => {
  it('round-trips an object through rows and back', () => {
    const rows = mapValue({ team: 'sre', env: 'prod' });
    expect(rows).toEqual(
      expect.arrayContaining([
        { key: 'team', value: 'sre' },
        { key: 'env', value: 'prod' },
      ]),
    );
    expect(mapFromRows(rows)).toEqual({ team: 'sre', env: 'prod' });
  });

  it('adds a blank row, edits it, then drops it if the key stays blank', () => {
    let rows = addMapRow([]);
    expect(rows).toEqual([{ key: '', value: '' }]);
    expect(mapFromRows(rows)).toEqual({});
    rows = setMapRow(rows, 0, { key: 'region' });
    rows = setMapRow(rows, 0, { value: 'us-east-1' });
    expect(mapFromRows(rows)).toEqual({ region: 'us-east-1' });
  });

  it('removes a row by index', () => {
    const rows = removeMapRow(
      [
        { key: 'a', value: '1' },
        { key: 'b', value: '2' },
      ],
      0,
    );
    expect(rows).toEqual([{ key: 'b', value: '2' }]);
  });

  it('a later duplicate key wins, matching object-literal semantics', () => {
    expect(
      mapFromRows([
        { key: 'a', value: '1' },
        { key: 'a', value: '2' },
      ]),
    ).toEqual({ a: '2' });
  });
});

describe('formatDefault', () => {
  it('renders scalar, list and map defaults readably', () => {
    expect(formatDefault('30s')).toBe('30s');
    expect(formatDefault(true)).toBe('true');
    expect(formatDefault(10000)).toBe('10000');
    expect(formatDefault(['a', 'b'])).toBe('[a, b]');
    expect(formatDefault({ x: 1 })).toBe('{x: 1}');
  });

  it('renders nothing for an absent default', () => {
    expect(formatDefault(undefined)).toBe('');
  });
});

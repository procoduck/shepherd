import { describe, expect, it } from 'vitest';
import {
  appendItem,
  blockInstances,
  commitInstances,
  nextBlockOrder,
  removeAt,
  replaceAt,
  withAttr,
} from './blockOps';

describe('blockInstances (D2 — object=one, array=many, else=none)', () => {
  it('reads a single-block object as one instance', () => {
    expect(blockInstances({ url: 'http://x' })).toEqual([{ url: 'http://x' }]);
  });

  it('reads a repeatable-block array as many instances, dropping non-object entries', () => {
    expect(blockInstances([{ a: 1 }, { b: 2 }, 'stray', null])).toEqual([{ a: 1 }, { b: 2 }]);
  });

  it('reads an absent or malformed value as zero instances (D4: degrade, never throw)', () => {
    expect(blockInstances(undefined)).toEqual([]);
    expect(blockInstances(null)).toEqual([]);
    expect(blockInstances('oops')).toEqual([]);
    expect(blockInstances(5)).toEqual([]);
  });
});

describe('withAttr', () => {
  it('sets a value immutably', () => {
    const instance = { a: 1 };
    const next = withAttr(instance, 'b', 2);
    expect(next).toEqual({ a: 1, b: 2 });
    expect(instance).toEqual({ a: 1 }); // original untouched
  });

  it('deletes the key when the value is undefined (an empty box is not a value)', () => {
    const next = withAttr({ a: 1, b: 2 }, 'b', undefined);
    expect(next).toEqual({ a: 1 });
  });

  it('is a no-op deleting a key that was never set', () => {
    const instance = { a: 1 };
    expect(withAttr(instance, 'missing', undefined)).toEqual(instance);
  });

  it('stores non-scalar values (a number, a list, a nested object) as their real type', () => {
    expect(withAttr({}, 'n', 5000).n).toBe(5000);
    expect(withAttr({}, 'tags', ['a', 'b']).tags).toEqual(['a', 'b']);
    expect(withAttr({}, 'nested', { x: 1 }).nested).toEqual({ x: 1 });
  });
});

describe('array helpers', () => {
  it('replaceAt / removeAt / appendItem', () => {
    expect(replaceAt([1, 2, 3], 1, 20)).toEqual([1, 20, 3]);
    expect(removeAt([1, 2, 3], 1)).toEqual([1, 3]);
    expect(appendItem([1, 2], 3)).toEqual([1, 2, 3]);
  });
});

describe('commitInstances — round trip through add/edit/remove', () => {
  it('a non-repeatable block commits its single instance as an object', () => {
    expect(commitInstances([{ url: 'http://x' }], false)).toEqual({ url: 'http://x' });
  });

  it('a repeatable block commits its instances as an array, even with one', () => {
    expect(commitInstances([{ action: 'keep' }], true)).toEqual([{ action: 'keep' }]);
  });

  it('zero instances commits to undefined — the block key is dropped, not emitted empty', () => {
    expect(commitInstances([], false)).toBeUndefined();
    expect(commitInstances([], true)).toBeUndefined();
  });

  it('full editing round trip: add an instance, edit it, add another, remove the first', () => {
    let instances = blockInstances(undefined);
    // add
    instances = appendItem(instances, {});
    instances = replaceAt(instances, 0, withAttr(instances[0], 'action', 'keep'));
    let value = commitInstances(instances, true);
    expect(value).toEqual([{ action: 'keep' }]);

    // add a second instance
    instances = blockInstances(value);
    instances = appendItem(instances, {});
    instances = replaceAt(instances, 1, withAttr(instances[1], 'action', 'drop'));
    value = commitInstances(instances, true);
    expect(value).toEqual([{ action: 'keep' }, { action: 'drop' }]);

    // remove the first
    instances = blockInstances(value);
    instances = removeAt(instances, 0);
    value = commitInstances(instances, true);
    expect(value).toEqual([{ action: 'drop' }]);

    // remove the last remaining instance — the block disappears entirely
    instances = blockInstances(value);
    instances = removeAt(instances, 0);
    value = commitInstances(instances, true);
    expect(value).toBeUndefined();
  });

  it('a non-repeatable block round trips through add/edit/remove as a single object', () => {
    let instances = blockInstances(undefined);
    instances = appendItem(instances, withAttr({}, 'url', 'http://x'));
    let value = commitInstances(instances, false);
    expect(value).toEqual({ url: 'http://x' });

    instances = blockInstances(value);
    instances = replaceAt(instances, 0, withAttr(instances[0], 'url', 'http://y'));
    value = commitInstances(instances, false);
    expect(value).toEqual({ url: 'http://y' });

    instances = blockInstances(value);
    instances = removeAt(instances, 0);
    value = commitInstances(instances, false);
    expect(value).toBeUndefined();
  });
});

describe('nextBlockOrder — the order props cannot hold', () => {
  it('appends a newly added block where the user put it: last', () => {
    expect(nextBlockOrder(['stage.json'], 'stage.drop', { source: 'level' }, true)).toEqual([
      'stage.json',
      'stage.drop',
    ]);
  });

  it('starts an order from nothing when the first block is added', () => {
    expect(nextBlockOrder(undefined, 'stage.json', { expressions: {} }, true)).toEqual([
      'stage.json',
    ]);
  });

  it('drops a removed block from the order', () => {
    // commitInstances returns undefined when the last instance goes.
    expect(nextBlockOrder(['stage.json', 'stage.drop'], 'stage.drop', undefined, true)).toEqual([
      'stage.json',
    ]);
  });

  it('leaves the order alone when an existing block is merely edited', () => {
    // Re-appending on every keystroke would be harmless but wrong; identity
    // here is what lets the caller skip a store write.
    const order = ['stage.json', 'stage.drop'];
    expect(nextBlockOrder(order, 'stage.json', { expressions: { a: 'b' } }, true)).toBe(order);
  });

  it('ignores attribute writes entirely', () => {
    // forward_to is an attribute, not a block: it must never enter block_order.
    const order = ['stage.json'];
    expect(nextBlockOrder(order, 'forward_to', ['x'], false)).toBe(order);
  });

  it('treats an emptied repeatable block as removed', () => {
    expect(nextBlockOrder(['rule', 'stage.json'], 'rule', [], true)).toEqual(['stage.json']);
  });
});

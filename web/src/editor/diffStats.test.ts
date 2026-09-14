import { describe, expect, it } from 'vitest';
import { diffStats } from './diffStats';

describe('diffStats', () => {
  it('identical texts have no additions or removals', () => {
    const text = 'prometheus.exporter.self "e2e" { }\noutput { }';
    expect(diffStats(text, text)).toEqual({ added: 0, removed: 0 });
  });

  it('one inserted line counts as one addition', () => {
    const oldText = 'line a\nline b';
    const newText = 'line a\nline b\nline c';
    expect(diffStats(oldText, newText)).toEqual({ added: 1, removed: 0 });
  });

  it('a replaced line counts as one addition and one removal', () => {
    const oldText = 'line a\nline b\nline c';
    const newText = 'line a\nline X\nline c';
    expect(diffStats(oldText, newText)).toEqual({ added: 1, removed: 1 });
  });
});

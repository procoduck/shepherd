import { describe, expect, it } from 'vitest';
import { rankPaletteItems } from './paletteSearch';

// F12: Palette.tsx used to filter with `includes()` and keep schema object
// order, so a fuzzy doc-text match could out-rank the component whose name
// the query names exactly. These two items mirror the real shipped schema
// (internal/schema/artifacts/overlay.json): `prometheus.receive_http`'s doc
// is "Receives Prometheus metrics via HTTP remote_write." (a fuzzy, doc-only
// match on the query below) while `prometheus.remote_write` matches by name.
const receiveHttp = {
  name: 'prometheus.receive_http',
  doc: 'Receives Prometheus metrics via HTTP remote_write.',
};
const remoteWrite = {
  name: 'prometheus.remote_write',
  doc: 'Sends metrics to a Prometheus remote_write endpoint.',
};

describe('rankPaletteItems', () => {
  it('ranks a name-segment match above a doc-only fuzzy match', () => {
    const ranked = rankPaletteItems('remote_write', [receiveHttp, remoteWrite]);
    expect(ranked.map((i) => i.name)).toEqual([
      'prometheus.remote_write',
      'prometheus.receive_http',
    ]);
  });

  it('ranks an exact name match first, ahead of a segment or prefix match', () => {
    const ranked = rankPaletteItems('prometheus.scrape', [
      { name: 'prometheus.scrape.something', doc: '' },
      { name: 'prometheus.scrape', doc: '' },
    ]);
    expect(ranked[0].name).toBe('prometheus.scrape');
  });

  it('ranks a name-segment match ahead of a mere prefix match', () => {
    // Regression for a bug where nameSegments() split on `_` as well as `.`,
    // so a query containing `_` (like `remote_write`) could never equal a
    // whole segment and this tier never fired. `remote_writer.x` only
    // *starts with* the query (prefix, tier 2); `prometheus.remote_write`
    // has `remote_write` as a whole `.`-delimited segment (tier 1) and must
    // rank first.
    const ranked = rankPaletteItems('remote_write', [
      { name: 'remote_writer.x', doc: '' },
      { name: 'prometheus.remote_write', doc: '' },
    ]);
    expect(ranked.map((i) => i.name)).toEqual(['prometheus.remote_write', 'remote_writer.x']);
  });

  it('sorts ties within a tier alphabetically by name', () => {
    const ranked = rankPaletteItems('loki', [
      { name: 'loki.write', doc: '' },
      { name: 'loki.process', doc: '' },
    ]);
    expect(ranked.map((i) => i.name)).toEqual(['loki.process', 'loki.write']);
  });

  it('ranks a `.`-segment match ahead of a substring match inside a `_`-joined segment', () => {
    // `.`-only splitting means `write` is a whole segment of `loki.write`
    // (tier 1) but only a substring of `remote_write`, the sole segment of
    // `prometheus.remote_write` (tier 3, since `_` no longer splits — see
    // the test above). This is deliberate: `loki.write` is the better match
    // for `write`, but it's a ranking change beyond the bug the `_`-split
    // removal fixed, so pin it explicitly.
    const ranked = rankPaletteItems('write', [
      { name: 'prometheus.remote_write', doc: '' },
      { name: 'loki.write', doc: '' },
    ]);
    expect(ranked.map((i) => i.name)).toEqual(['loki.write', 'prometheus.remote_write']);
  });

  it('drops items that match neither name nor doc', () => {
    const ranked = rankPaletteItems('remote_write', [{ name: 'discovery.kubernetes', doc: '' }]);
    expect(ranked).toEqual([]);
  });

  it('returns items unchanged for an empty query', () => {
    const items = [receiveHttp, remoteWrite];
    expect(rankPaletteItems('', items)).toBe(items);
  });
});

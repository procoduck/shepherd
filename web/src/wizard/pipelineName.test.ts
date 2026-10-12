import { describe, expect, it } from 'vitest';
import { suggestPipelineName, uniquePipelineName } from './pipelineName';

// #306: every wizard gets a default pipeline name, unique in the org.
describe('suggestPipelineName', () => {
  it('uses the wizard kind alone when there is no job name', () => {
    expect(suggestPipelineName('pod-logs', undefined)).toBe('pod-logs');
    expect(suggestPipelineName('pod-logs', '')).toBe('pod-logs');
  });

  it('appends a job name that adds something', () => {
    expect(suggestPipelineName('app-observability', 'My App')).toBe('app-observability-my-app');
  });

  it('does not repeat a job name the kind already says', () => {
    expect(suggestPipelineName('blackbox', 'blackbox')).toBe('blackbox');
    expect(suggestPipelineName('cluster-metrics', 'cluster-metrics')).toBe('cluster-metrics');
  });
});

describe('uniquePipelineName', () => {
  it('keeps a free name', () => {
    expect(uniquePipelineName('pod-logs', ['other'])).toBe('pod-logs');
  });

  it('adds the first free numeric suffix to a taken name', () => {
    expect(uniquePipelineName('pod-logs', ['pod-logs'])).toBe('pod-logs-2');
    expect(uniquePipelineName('pod-logs', ['pod-logs', 'pod-logs-2', 'pod-logs-3'])).toBe(
      'pod-logs-4',
    );
  });
});

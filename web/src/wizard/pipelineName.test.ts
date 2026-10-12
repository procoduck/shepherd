import { describe, expect, it } from 'vitest';
import {
  isNameTaken,
  sanitizePipelineName,
  suggestPipelineName,
  takenNameKeys,
  uniquePipelineName,
} from './pipelineName';

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

// The server's unique index is on the sanitized name (migration 0002), so a
// name that differs only in case or punctuation is taken too.
describe('the sanitized comparison', () => {
  it('mirrors sanitize_pipeline_name', () => {
    expect(sanitizePipelineName('Pod Logs')).toBe('pod_logs');
    expect(sanitizePipelineName('pod-logs')).toBe('pod_logs');
    expect(sanitizePipelineName('9lives')).toBe('p9lives');
  });

  it('treats "Pod Logs" and "pod_logs" as taking "pod-logs"', () => {
    expect(uniquePipelineName('pod-logs', ['Pod Logs'])).toBe('pod-logs-2');
    expect(uniquePipelineName('pod-logs', ['pod_logs', 'POD-LOGS-2'])).toBe('pod-logs-3');
    expect(isNameTaken('pod-logs', takenNameKeys(['pod_logs']))).toBe(true);
    expect(isNameTaken('pod-logs', takenNameKeys(['pod-logs-x']))).toBe(false);
  });
});

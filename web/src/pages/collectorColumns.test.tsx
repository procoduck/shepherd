// @vitest-environment jsdom
import { create } from '@bufbuild/protobuf';
import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { CollectorInstanceSchema } from '@/gen/shepherd/mgmt/v1/fleet_pb';
import { instanceColumns } from './collectorColumns';

const statusCell = instanceColumns.find((c) => c.key === 'status');

function renderStatus(remoteConfigStatus: string) {
  if (!statusCell) throw new Error('no status column');
  render(<div>{statusCell.render(create(CollectorInstanceSchema, { remoteConfigStatus }))}</div>);
}

describe('instance status badge', () => {
  it('explains a server-derived INACTIVE status on hover (#237)', () => {
    renderStatus('inactive');
    expect(screen.getByText('INACTIVE').getAttribute('title')).toMatch(/agent\.inactive_after/);
  });

  it('adds no hover text to an agent-reported status', () => {
    renderStatus('FAILED');
    expect(screen.getByText('FAILED').getAttribute('title')).toBeNull();
  });
});

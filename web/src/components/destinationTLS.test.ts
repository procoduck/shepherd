import { describe, expect, it } from 'vitest';
import {
  EMPTY_TLS,
  extraWithTLS,
  k8sFieldError,
  tlsFromExtra,
  tlsIsSet,
  tlsSummary,
} from './DestinationTLSFields';

// extra.tls (#261) must round-trip exactly what internal/wizard.ParseTLS
// accepts: strict keys, the CA key only when it overrides ca.crt.
describe('destination TLS extra', () => {
  it('removes tls when nothing is set, keeping other extra keys', () => {
    expect(extraWithTLS({ owner: 'x', tls: { server_name: 'old' } }, EMPTY_TLS)).toEqual({
      owner: 'x',
    });
    expect(tlsIsSet(EMPTY_TLS)).toBe(false);
  });

  it('writes a ConfigMap CA without a key when it is the default', () => {
    const extra = extraWithTLS(
      {},
      {
        ...EMPTY_TLS,
        caKind: 'configmap',
        caNamespace: 'monitoring',
        caName: 'ca',
        caKey: 'ca.crt',
      },
    );
    expect(extra).toEqual({
      tls: { ca: { kind: 'configmap', namespace: 'monitoring', name: 'ca' } },
    });
  });

  it('writes an overridden CA key, a client cert and a server name, and reads them back', () => {
    const form = {
      ...EMPTY_TLS,
      caKind: 'secret' as const,
      caNamespace: 'cert-manager',
      caName: 'org-trust',
      caKey: 'trust-bundle.pem',
      clientCert: true,
      clientNamespace: 'monitoring',
      clientName: 'collector-mtls',
      serverName: 'mimir.internal.example',
    };
    const extra = extraWithTLS({ oauth2_scopes: ['a'] }, form);
    expect(extra).toEqual({
      oauth2_scopes: ['a'],
      tls: {
        ca: {
          kind: 'secret',
          namespace: 'cert-manager',
          name: 'org-trust',
          key: 'trust-bundle.pem',
        },
        client_cert: { namespace: 'monitoring', name: 'collector-mtls' },
        server_name: 'mimir.internal.example',
      },
    });
    expect(tlsFromExtra(extra)).toEqual(form);
  });

  it('reads a missing or malformed tls as empty', () => {
    expect(tlsFromExtra(undefined)).toEqual(EMPTY_TLS);
    expect(tlsFromExtra({ tls: 'yes' })).toEqual(EMPTY_TLS);
  });
});

describe('k8sFieldError (mirrors the server rules)', () => {
  it('accepts valid namespaces, names and keys, and empty fields', () => {
    expect(k8sFieldError('namespace', 'monitoring')).toBeUndefined();
    expect(k8sFieldError('name', 'backend-ca.v2')).toBeUndefined();
    expect(k8sFieldError('key', 'trust-bundle.pem')).toBeUndefined();
    expect(k8sFieldError('namespace', '')).toBeUndefined();
  });
  it('refuses what the server refuses', () => {
    expect(k8sFieldError('namespace', 'Monitoring')).toMatch(/namespace/);
    expect(k8sFieldError('namespace', 'a.b')).toMatch(/namespace/);
    expect(k8sFieldError('name', 'backend_ca')).toMatch(/object name/);
    expect(k8sFieldError('key', 'ca crt')).toMatch(/data key/);
  });
});

// 2026-10-12 walkthrough: the destinations table did not say which
// destinations carry TLS options. The badge's description reads them back.
describe('tlsSummary', () => {
  it('is empty when no TLS option is set', () => {
    expect(tlsSummary(EMPTY_TLS)).toBe('');
  });

  it('names the CA, the client certificate and the server name', () => {
    expect(
      tlsSummary(
        tlsFromExtra({
          tls: {
            ca: { kind: 'configmap', namespace: 'cert-manager', name: 'org-trust' },
            client_cert: { namespace: 'monitoring', name: 'collector-mtls' },
            server_name: 'mimir.internal',
          },
        }),
      ),
    ).toBe(
      'CA from ConfigMap cert-manager/org-trust; client certificate from Secret monitoring/collector-mtls; server name mimir.internal',
    );
    expect(tlsSummary(tlsFromExtra({ tls: { server_name: 'x' } }))).toBe('server name x');
  });
});

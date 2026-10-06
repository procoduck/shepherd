import { describe, expect, it } from 'vitest';
import { EMPTY_TLS, extraWithTLS, tlsFromExtra, tlsIsSet } from './DestinationTLSFields';

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

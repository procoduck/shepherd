import type { JsonObject } from '@bufbuild/protobuf';
import { useState } from 'react';
import { Field, Input, Select } from '@/components/ui/Field';

/*
 * The destination's TLS options (#261), stored as extra.tls — mirrors
 * wizard.TLS in internal/wizard/tls.go, which validates and renders them.
 * Shepherd stores only references to a Secret/ConfigMap on each spoke
 * cluster and key names, never certificate material.
 */

/** destinations.extra key holding the TLS options. */
const TLS_KEY = 'tls';
/** The CA key used when the destination names none (wizard.TLSKeyCADefault). */
export const DEFAULT_CA_KEY = 'ca.crt';

export interface TLSFormState {
  /** '' = the system trust store. */
  caKind: '' | 'configmap' | 'secret';
  caNamespace: string;
  caName: string;
  /** Empty = ca.crt. */
  caKey: string;
  clientCert: boolean;
  clientNamespace: string;
  clientName: string;
  serverName: string;
}

export const EMPTY_TLS: TLSFormState = {
  caKind: '',
  caNamespace: '',
  caName: '',
  caKey: '',
  clientCert: false,
  clientNamespace: '',
  clientName: '',
  serverName: '',
};

function str(o: unknown, k: string): string {
  if (o && typeof o === 'object' && !Array.isArray(o)) {
    const v = (o as Record<string, unknown>)[k];
    return typeof v === 'string' ? v : '';
  }
  return '';
}

export function tlsFromExtra(extra: JsonObject | undefined): TLSFormState {
  const t = extra?.[TLS_KEY];
  if (!t || typeof t !== 'object' || Array.isArray(t)) return EMPTY_TLS;
  const ca = (t as Record<string, unknown>).ca;
  const client = (t as Record<string, unknown>).client_cert;
  const kind = str(ca, 'kind');
  return {
    caKind: kind === 'configmap' || kind === 'secret' ? kind : '',
    caNamespace: str(ca, 'namespace'),
    caName: str(ca, 'name'),
    caKey: str(ca, 'key'),
    clientCert: !!client && typeof client === 'object',
    clientNamespace: str(client, 'namespace'),
    clientName: str(client, 'name'),
    serverName: str(t, 'server_name'),
  };
}

/** Whether the form asks for any TLS option at all. */
export function tlsIsSet(t: TLSFormState): boolean {
  return t.caKind !== '' || t.clientCert || t.serverName.trim() !== '';
}

/**
 * The extra to send: the stored one with `tls` replaced by the form's
 * options, or removed when none is set.
 */
export function extraWithTLS(stored: JsonObject, t: TLSFormState): JsonObject {
  const { [TLS_KEY]: _stale, ...rest } = stored;
  if (!tlsIsSet(t)) return rest;
  const tls: JsonObject = {};
  if (t.caKind !== '') {
    const ca: JsonObject = {
      kind: t.caKind,
      namespace: t.caNamespace.trim(),
      name: t.caName.trim(),
    };
    if (t.caKey.trim() !== '' && t.caKey.trim() !== DEFAULT_CA_KEY) ca.key = t.caKey.trim();
    tls.ca = ca;
  }
  if (t.clientCert) {
    tls.client_cert = { namespace: t.clientNamespace.trim(), name: t.clientName.trim() };
  }
  if (t.serverName.trim() !== '') tls.server_name = t.serverName.trim();
  return { ...rest, [TLS_KEY]: tls };
}

export function DestinationTLSFields({
  value,
  onChange,
}: {
  value: TLSFormState;
  onChange: (next: TLSFormState) => void;
}) {
  const set = (patch: Partial<TLSFormState>) => onChange({ ...value, ...patch });
  // Collapsed unless the destination already has TLS options, so most
  // destinations' dialogs stay short.
  const [open, setOpen] = useState(() => tlsIsSet(value));
  if (!open) {
    return (
      <button
        type='button'
        onClick={() => setOpen(true)}
        className='text-xs text-indigo-400 hover:text-indigo-300'
      >
        TLS options (private CA, client certificate)…
      </button>
    );
  }
  return (
    <fieldset className='space-y-3 rounded-md border border-border px-3 py-3'>
      <legend className='px-1 text-xs font-medium text-muted'>TLS</legend>
      <p data-testid='tls-explanation' className='text-2xs text-muted'>
        For a backend behind a private CA or one that requires a client certificate. The collector
        reads the certificates from a Secret or ConfigMap on each spoke cluster; Shepherd stores
        only where they are. A client certificate Secret holds{' '}
        <code className='font-mono'>tls.crt</code> and <code className='font-mono'>tls.key</code> (a{' '}
        <code className='font-mono'>kubernetes.io/tls</code> Secret, as cert-manager writes). Needs
        an <code className='font-mono'>https://</code> URL.
      </p>
      <Field label='Trusted CA'>
        <Select
          value={value.caKind}
          onChange={(e) => set({ caKind: e.target.value as TLSFormState['caKind'] })}
        >
          <option value=''>System trust store</option>
          <option value='configmap'>From a ConfigMap</option>
          <option value='secret'>From a Secret</option>
        </Select>
      </Field>
      {value.caKind !== '' && (
        <div className='flex gap-3'>
          <Field label='CA namespace' className='flex-1'>
            <Input
              value={value.caNamespace}
              onChange={(e) => set({ caNamespace: e.target.value })}
              required
              mono
              placeholder='monitoring'
            />
          </Field>
          <Field label='CA name' className='flex-1'>
            <Input
              value={value.caName}
              onChange={(e) => set({ caName: e.target.value })}
              required
              mono
              placeholder='backend-ca'
            />
          </Field>
          <Field label='CA key' className='flex-1' optional>
            <Input
              value={value.caKey}
              onChange={(e) => set({ caKey: e.target.value })}
              mono
              placeholder={DEFAULT_CA_KEY}
            />
          </Field>
        </div>
      )}
      <label className='flex items-center gap-2 text-xs'>
        <input
          type='checkbox'
          checked={value.clientCert}
          onChange={(e) => set({ clientCert: e.target.checked })}
        />
        Present a client certificate
      </label>
      {value.clientCert && (
        <div className='flex gap-3'>
          <Field label='Client certificate Secret namespace' className='flex-1'>
            <Input
              value={value.clientNamespace}
              onChange={(e) => set({ clientNamespace: e.target.value })}
              required
              mono
              placeholder='monitoring'
            />
          </Field>
          <Field label='Client certificate Secret name' className='flex-1'>
            <Input
              value={value.clientName}
              onChange={(e) => set({ clientName: e.target.value })}
              required
              mono
              placeholder='collector-mtls'
            />
          </Field>
        </div>
      )}
      <Field label='Server name' optional>
        <Input
          value={value.serverName}
          onChange={(e) => set({ serverName: e.target.value })}
          mono
          placeholder='mimir.internal.example'
        />
      </Field>
    </fieldset>
  );
}

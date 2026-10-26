import { describe, expect, it } from 'vitest';
import { importRepositoryCredential, importRepositoryError } from './importRepos';

describe('import repositories', () => {
  it('accepts the git remote forms the server takes', () => {
    for (const v of [
      'https://github.com/acme/modules.git',
      'http://gitea.internal:3000/acme/modules',
      'ssh://git@github.com/acme/modules.git',
      'git@github.com:acme/modules.git',
      'github.com:acme/modules',
    ]) {
      expect(importRepositoryError(v), v).toBe('');
    }
  });

  it('refuses what cannot be a git remote', () => {
    expect(importRepositoryError('')).toBe('Enter the repository URL');
    expect(importRepositoryError(' https://github.com/acme/m.git')).toContain('spaces');
    for (const v of ['file:///srv/m.git', '/srv/m.git', 'https://github.com', 'not a url']) {
      expect(importRepositoryError(v), v).toContain('Use https://');
    }
  });

  it('shows the credential name, or public', () => {
    expect(importRepositoryCredential({ credentialId: '', credentialName: '' })).toBe('public');
    expect(importRepositoryCredential({ credentialId: 'c1', credentialName: 'deploy' })).toBe(
      'deploy',
    );
    expect(importRepositoryCredential({ credentialId: 'c1', credentialName: '' })).toBe('c1');
  });
});

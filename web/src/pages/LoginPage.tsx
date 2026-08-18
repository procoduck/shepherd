import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { Shield } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useAuthMethods } from '@/hooks/useAuthMethods';
import { useMe } from '@/hooks/useMe';

export function LoginPage() {
  const { data: me, isLoading } = useMe();
  const { data: methods, isLoading: methodsLoading } = useAuthMethods();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [loginError, setLoginError] = useState(false);

  useEffect(() => {
    if (!isLoading && me !== null && me !== undefined) {
      navigate({ to: '/' });
    }
  }, [me, isLoading, navigate]);

  async function handleLocalLogin(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setLoginError(false);
    const response = await fetch('/api/auth/local/login', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Requested-With': 'XMLHttpRequest',
      },
      body: JSON.stringify({ username, password }),
    });
    if (!response.ok) {
      setLoginError(true);
      return;
    }
    queryClient.clear();
    navigate({ to: '/' });
  }

  if (methodsLoading) {
    return (
      <div className='min-h-screen bg-zinc-950 flex items-center justify-center text-sm text-zinc-400'>
        Loading sign-in options...
      </div>
    );
  }

  const oidc = methods?.oidc === true;
  const localAdmin = methods?.local_admin === true;
  return (
    <div className='min-h-screen bg-zinc-950 flex items-center justify-center'>
      <div className='w-full max-w-sm rounded-lg border border-zinc-800 bg-zinc-900 p-6 space-y-6'>
        <div className='flex flex-col items-center gap-3'>
          <Shield size={32} className='text-indigo-500' />
          <h1 className='text-lg font-semibold text-zinc-100'>Sign in to Shepherd</h1>
        </div>

        {window.location.search.includes('auth_error') && (
          <div className='rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400'>
            Authentication failed. Please try again.
          </div>
        )}

        {oidc && (
          <>
            <a
              data-testid='oidc-login-btn'
              href='/auth/login'
              className='flex w-full items-center justify-center gap-2 rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-500 transition-colors'
            >
              Continue with Microsoft
            </a>
            <p className='text-center text-xs text-zinc-500'>
              Access is managed through your Entra ID groups.
            </p>
          </>
        )}

        {oidc && localAdmin && <div className='text-center text-xs text-zinc-500'>or</div>}

        {localAdmin && (
          <form onSubmit={handleLocalLogin} className='space-y-3'>
            <input
              data-testid='local-username'
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              placeholder='Username'
              autoComplete='username'
              className='w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100'
            />
            <input
              data-testid='local-password'
              type='password'
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              placeholder='Password'
              autoComplete='current-password'
              className='w-full rounded-md border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-zinc-100'
            />
            <button
              data-testid='local-login-submit'
              type='submit'
              className='w-full rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-500 transition-colors'
            >
              Sign in
            </button>
            {loginError && (
              <p data-testid='local-login-error' className='text-sm text-red-400'>
                Invalid username or password
              </p>
            )}
          </form>
        )}

        {!oidc && !localAdmin && (
          <div
            data-testid='no-methods-card'
            className='rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400'
          >
            No sign-in methods are currently available.
          </div>
        )}
      </div>
    </div>
  );
}

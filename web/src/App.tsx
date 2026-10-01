import { useCallback, useEffect, useState } from 'react';
import { api } from './api';
import { Login, Setup } from './Auth';
import { Mail } from './Mail';
import { Spinner } from './ui';

type State = 'loading' | 'setup' | 'login' | 'app' | 'error';

export function App() {
  const [state, setState] = useState<State>('loading');

  const check = useCallback(async () => {
    try {
      const s = await api.get<{ setup_required: boolean; authenticated: boolean }>('/auth/state');
      setState(s.setup_required ? 'setup' : s.authenticated ? 'app' : 'login');
    } catch {
      setState('error');
    }
  }, []);

  useEffect(() => {
    check();
    const onUnauthorized = () => setState('login');
    window.addEventListener('inbox:unauthorized', onUnauthorized);
    return () => window.removeEventListener('inbox:unauthorized', onUnauthorized);
  }, [check]);

  if (state === 'loading') {
    return (
      <div className="flex min-h-[100dvh] items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (state === 'error') {
    return (
      <div className="flex min-h-[100dvh] flex-col items-center justify-center gap-3 text-sm text-ink-2">
        Server unreachable.
        <button className="font-medium text-accent" onClick={check}>
          Retry
        </button>
      </div>
    );
  }
  if (state === 'setup') return <Setup onDone={check} />;
  if (state === 'login') return <Login onDone={check} />;
  return <Mail />;
}

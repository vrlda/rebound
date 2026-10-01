import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import QRCode from 'qrcode';
import { ShieldCheck } from 'lucide-react';
import { api, ApiError } from './api';
import { Button, ErrorText, Field, Input } from './ui';

function Shell({ title, subtitle, children }: { title: string; subtitle: string; children: ReactNode }) {
  return (
    <main className="flex min-h-[100dvh] items-center justify-center bg-canvas px-4 py-10">
      <div className="animate-rise w-full max-w-sm">
        <div className="mb-8 flex items-center gap-2.5">
          <img src="/icon.svg" alt="" className="size-8 rounded-lg" />
          <span className="text-[15px] font-semibold tracking-tight">Rebound</span>
        </div>
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        <p className="mt-1.5 text-sm text-ink-2">{subtitle}</p>
        <div className="mt-7">{children}</div>
      </div>
    </main>
  );
}

const codeProps = {
  inputMode: 'numeric' as const,
  autoComplete: 'one-time-code',
  pattern: '[0-9 ]*',
  maxLength: 7,
  placeholder: '123 456',
  className: 'font-mono tracking-[0.3em]',
};

export function Login({ onDone }: { onDone: () => void }) {
  const [password, setPassword] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.post('/auth/login', { password, code });
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not sign in');
      setCode('');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Shell title="Sign in" subtitle="Password and a code from your authenticator app.">
      <form onSubmit={submit} className="space-y-4">
        <Field label="Password">
          <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus required />
        </Field>
        <Field label="2FA code">
          <Input {...codeProps} value={code} onChange={(e) => setCode(e.target.value)} required />
        </Field>
        <ErrorText>{error}</ErrorText>
        <Button variant="primary" type="submit" loading={busy} className="w-full">
          Sign in
        </Button>
      </form>
    </Shell>
  );
}

export function Setup({ onDone }: { onDone: () => void }) {
  const token = window.location.hash.slice(1);
  const [qr, setQr] = useState('');
  const [secret, setSecret] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!token) {
      setError('This page needs the one-time setup link.');
      return;
    }
    api
      .post<{ otpauth_url: string; secret: string }>('/auth/setup/begin', { token })
      .then(async (r) => {
        setSecret(r.secret);
        setQr(await QRCode.toDataURL(r.otpauth_url, { margin: 1, width: 220 }));
      })
      .catch((err) => setError(err instanceof ApiError ? err.message : 'Setup failed'));
  }, [token]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError('Passwords do not match');
      return;
    }
    setBusy(true);
    setError('');
    try {
      await api.post('/auth/setup/finish', { token, password, code });
      history.replaceState(null, '', '/');
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Setup failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Shell title="Secure your inbox" subtitle="Set a password and connect an authenticator app. This link works once.">
      <form onSubmit={submit} className="space-y-5">
        <div className="rounded-xl border border-line bg-surface p-4">
          <div className="flex items-center gap-2 text-[13px] font-medium text-ink-2">
            <ShieldCheck className="size-4 text-ok" aria-hidden />
            Scan with Google Authenticator, 1Password, Authy…
          </div>
          <div className="mt-3 flex items-center justify-center rounded-lg bg-white p-2">
            {qr ? <img src={qr} alt="2FA QR code" className="size-[200px]" /> : <div className="size-[200px]" />}
          </div>
          {secret && (
            <p className="mt-3 break-all text-center font-mono text-xs text-ink-3">
              or enter key: <span className="select-all text-ink-2">{secret}</span>
            </p>
          )}
        </div>
        <Field label="Password" hint="At least 12 characters. A passphrase is easiest.">
          <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={12} />
        </Field>
        <Field label="Confirm password">
          <Input type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />
        </Field>
        <Field label="Code from the app">
          <Input {...codeProps} value={code} onChange={(e) => setCode(e.target.value)} required />
        </Field>
        <ErrorText>{error}</ErrorText>
        <Button variant="primary" type="submit" loading={busy} disabled={!secret} className="w-full">
          Finish setup
        </Button>
      </form>
    </Shell>
  );
}

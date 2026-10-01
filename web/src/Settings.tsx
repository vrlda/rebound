import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { ArrowLeft, Bell, Plus, RefreshCw, Trash2 } from 'lucide-react';
import { api, ApiError, type Account, type Rule } from './api';
import { cx, urlBase64ToUint8Array } from './lib';
import { Button, ErrorText, Field, IconButton, Input, useToast } from './ui';

const SWATCHES = ['#6d5dfc', '#0ea5e9', '#10b981', '#f59e0b', '#ef4444', '#ec4899', '#64748b'];

export function Settings({ accounts, onBack, onChanged }: { accounts: Account[]; onBack: () => void; onChanged: () => void }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex items-center gap-1 border-b border-line px-2 py-2 safe-top">
        <IconButton label="Back to mail" onClick={onBack}>
          <ArrowLeft className="size-5" />
        </IconButton>
        <h1 className="text-[15px] font-semibold tracking-tight">Settings</h1>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto scroll-thin">
        <div className="mx-auto max-w-2xl space-y-10 px-4 py-6 pb-24 md:px-8">
          <AccountsSection accounts={accounts} onChanged={onChanged} />
          <NotificationsSection />
          <RulesSection />
          <SecuritySection />
        </div>
      </div>
    </div>
  );
}

function Section({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <section>
      <h2 className="text-base font-semibold tracking-tight">{title}</h2>
      {description && <p className="mt-1 text-sm text-ink-2">{description}</p>}
      <div className="mt-4">{children}</div>
    </section>
  );
}

function AccountsSection({ accounts, onChanged }: { accounts: Account[]; onChanged: () => void }) {
  const [adding, setAdding] = useState(accounts.length === 0);
  return (
    <Section
      title="Resend accounts"
      description="Each account needs a Resend API key with full access (receiving + sending). Keys are encrypted at rest and never shown again."
    >
      <div className="space-y-3">
        {accounts.map((a) => (
          <AccountCard key={a.id} account={a} onChanged={onChanged} />
        ))}
        {adding ? (
          <AddAccount onDone={() => { setAdding(false); onChanged(); }} onCancel={accounts.length ? () => setAdding(false) : undefined} />
        ) : (
          <Button onClick={() => setAdding(true)}>
            <Plus className="size-4" /> Connect another account
          </Button>
        )}
      </div>
    </Section>
  );
}

function AddAccount({ onDone, onCancel }: { onDone: () => void; onCancel?: () => void }) {
  const toast = useToast();
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [color, setColor] = useState(SWATCHES[1]);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.post('/accounts', { name, api_key: key, color });
      toast('Account connected — syncing mail');
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not connect');
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit} className="space-y-4 rounded-xl border border-line bg-surface p-4">
      <Field label="Name">
        <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Personal site, Client X…" required />
      </Field>
      <Field label="Resend API key" hint="resend.com → API Keys → Create → Full access.">
        <Input value={key} onChange={(e) => setKey(e.target.value)} placeholder="re_…" autoComplete="off" spellCheck={false} className="font-mono" required />
      </Field>
      <ColorPicker value={color} onChange={setColor} />
      <ErrorText>{error}</ErrorText>
      <div className="flex gap-2">
        <Button variant="primary" type="submit" loading={busy}>
          Connect
        </Button>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        )}
      </div>
    </form>
  );
}

function ColorPicker({ value, onChange }: { value: string; onChange: (c: string) => void }) {
  return (
    <div className="flex items-center gap-2" role="radiogroup" aria-label="Color">
      {SWATCHES.map((c) => (
        <button
          key={c}
          type="button"
          role="radio"
          aria-checked={value === c}
          aria-label={c}
          onClick={() => onChange(c)}
          className={cx('size-6 rounded-full transition-transform', value === c ? 'scale-110 ring-2 ring-ink ring-offset-2 ring-offset-surface' : 'hover:scale-105')}
          style={{ background: c }}
        />
      ))}
    </div>
  );
}

function AccountCard({ account, onChanged }: { account: Account; onChanged: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(account.name);
  const [color, setColor] = useState(account.color);
  const [identities, setIdentities] = useState(account.identities.join('\n'));
  const [defaultFrom, setDefaultFrom] = useState(account.default_from);
  const [signature, setSignature] = useState(account.signature);
  const [newKey, setNewKey] = useState('');
  const [busy, setBusy] = useState(false);
  const [syncing, setSyncing] = useState(false);

  async function save() {
    setBusy(true);
    try {
      await api.patch(`/accounts/${account.id}`, {
        name,
        color,
        identities: identities.split(/[\n,]/).map((s) => s.trim()).filter(Boolean),
        default_from: defaultFrom,
        signature,
        ...(newKey.trim() ? { api_key: newKey.trim() } : {}),
      });
      setNewKey('');
      toast('Saved');
      onChanged();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : 'Save failed', 'error');
    } finally {
      setBusy(false);
    }
  }

  async function sync() {
    setSyncing(true);
    try {
      await api.post(`/accounts/${account.id}/sync`);
      onChanged();
      toast('Synced');
    } catch (err) {
      toast(err instanceof ApiError ? err.message : 'Sync failed', 'error');
    } finally {
      setSyncing(false);
    }
  }

  async function remove() {
    if (!confirm(`Disconnect "${account.name}" and delete its stored mail from this inbox? Mail in Resend itself is not touched.`)) return;
    await api.del(`/accounts/${account.id}`);
    toast('Account removed');
    onChanged();
  }

  return (
    <div className="space-y-4 rounded-xl border border-line bg-surface p-4">
      <div className="flex items-center gap-3">
        <span className="size-3 rounded-full" style={{ background: color }} />
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold">{account.name}</p>
          <p className="text-xs text-ink-3">
            Key {account.key_hint} · {account.last_sync_at ? `synced ${new Date(account.last_sync_at * 1000).toLocaleTimeString()}` : 'not synced yet'}
          </p>
        </div>
        <IconButton label="Sync now" onClick={sync} disabled={syncing}>
          <RefreshCw className={cx('size-4', syncing && 'animate-spin')} />
        </IconButton>
        <IconButton label="Remove account" onClick={remove} className="hover:text-danger">
          <Trash2 className="size-4" />
        </IconButton>
      </div>
      {account.last_error && <ErrorText>Last sync failed: {account.last_error}</ErrorText>}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name">
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Default From address">
          <Input value={defaultFrom} onChange={(e) => setDefaultFrom(e.target.value)} placeholder="hello@yourdomain.com" />
        </Field>
      </div>
      <Field label="Sending addresses" hint="One per line. Addresses you receive mail at are added automatically. The domain must be verified in this Resend account.">
        <textarea
          value={identities}
          onChange={(e) => setIdentities(e.target.value)}
          rows={3}
          className="w-full rounded-lg border border-line bg-surface px-3 py-2 font-mono text-[13px] outline-none focus:border-accent focus:ring-3 focus:ring-accent-soft"
        />
      </Field>
      <Field label="Signature">
        <textarea
          value={signature}
          onChange={(e) => setSignature(e.target.value)}
          rows={3}
          placeholder={'Jane Doe\nAcme Inc. — acme.com'}
          className="w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm outline-none focus:border-accent focus:ring-3 focus:ring-accent-soft"
        />
      </Field>
      <ColorPicker value={color} onChange={setColor} />
      <Field label="Replace API key" hint="Leave empty to keep the current key.">
        <Input value={newKey} onChange={(e) => setNewKey(e.target.value)} placeholder="re_…" autoComplete="off" spellCheck={false} className="font-mono" />
      </Field>
      <Button variant="primary" onClick={save} loading={busy}>
        Save changes
      </Button>
    </div>
  );
}

function NotificationsSection() {
  const toast = useToast();
  const supported = 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
  const [state, setState] = useState<NotificationPermission | 'unsupported'>(supported ? Notification.permission : 'unsupported');
  const [busy, setBusy] = useState(false);
  const isIOS = /iphone|ipad|ipod/i.test(navigator.userAgent);
  const standalone = window.matchMedia('(display-mode: standalone)').matches;

  async function enable() {
    setBusy(true);
    try {
      const permission = await Notification.requestPermission();
      setState(permission);
      if (permission !== 'granted') return;
      const reg = await navigator.serviceWorker.ready;
      const { public_key } = await api.get<{ public_key: string }>('/push/key');
      const sub =
        (await reg.pushManager.getSubscription()) ??
        (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(public_key) as BufferSource }));
      await api.post('/push/subscribe', sub.toJSON());
      const { delivered } = await api.post<{ delivered: number }>('/push/test');
      toast(delivered ? 'Notifications on — test sent' : 'Subscribed, but the test push did not arrive', delivered ? 'info' : 'error');
    } catch (err) {
      toast(err instanceof Error ? err.message : 'Could not enable notifications', 'error');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Section title="Notifications" description="Get a push notification for every new email that lands in Inbox (not spam).">
      {state === 'unsupported' ? (
        <p className="text-sm text-ink-2">
          {isIOS && !standalone
            ? 'On iPhone: tap Share → Add to Home Screen, open Inbox from the home screen, then enable notifications here.'
            : 'This browser does not support push notifications.'}
        </p>
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <Button onClick={enable} loading={busy} variant={state === 'granted' ? 'secondary' : 'primary'}>
            <Bell className="size-4" /> {state === 'granted' ? 'Re-subscribe this device & send test' : 'Enable on this device'}
          </Button>
          {state === 'denied' && <span className="text-sm text-danger">Blocked in browser settings — allow notifications for this site.</span>}
        </div>
      )}
    </Section>
  );
}

function RulesSection() {
  const [rules, setRules] = useState<Rule[]>([]);
  const [pattern, setPattern] = useState('');
  const load = () => api.get<Rule[]>('/rules').then(setRules).catch(() => {});
  useEffect(() => {
    load();
  }, []);
  async function add(action: 'spam' | 'inbox') {
    if (!pattern.includes('@')) return;
    await api.post('/rules', { pattern, action });
    setPattern('');
    load();
  }
  return (
    <Section title="Filters" description="Marking a message as spam blocks its sender's domain (or the exact address for Gmail/Outlook etc.). “Not spam” always allows the sender.">
      <div className="flex gap-2">
        <Input value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="someone@domain.com or @domain.com" />
        <Button onClick={() => add('spam')}>Block</Button>
        <Button onClick={() => add('inbox')}>Allow</Button>
      </div>
      {rules.length > 0 && (
        <ul className="mt-3 divide-y divide-line rounded-xl border border-line bg-surface">
          {rules.map((r) => (
            <li key={r.id} className="flex items-center gap-3 px-4 py-2.5 text-sm">
              <span className={cx('rounded px-1.5 py-0.5 text-[11px] font-semibold uppercase', r.action === 'spam' ? 'bg-danger-soft text-danger' : 'bg-accent-soft text-accent')}>
                {r.action === 'spam' ? 'Block' : 'Allow'}
              </span>
              <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{r.pattern}</span>
              <IconButton label="Delete filter" onClick={() => api.del(`/rules/${r.id}`).then(load)}>
                <Trash2 className="size-4" />
              </IconButton>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function SecuritySection() {
  const toast = useToast();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  async function change(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.post('/auth/password', { current, new: next, code });
      setCurrent('');
      setNext('');
      setCode('');
      toast('Password changed — other devices were signed out');
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed');
    } finally {
      setBusy(false);
    }
  }
  return (
    <Section title="Security" description="2FA is always required. Lost your authenticator? Run the reset command on the server (see README).">
      <form onSubmit={change} className="space-y-4 rounded-xl border border-line bg-surface p-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <Field label="Current password">
            <Input type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />
          </Field>
          <Field label="New password">
            <Input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} minLength={12} required />
          </Field>
          <Field label="2FA code">
            <Input inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} className="font-mono" required />
          </Field>
        </div>
        <ErrorText>{error}</ErrorText>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" loading={busy}>
            Change password
          </Button>
          <Button
            type="button"
            variant="danger"
            onClick={async () => {
              if (!confirm('Sign out of every device, including this one?')) return;
              await api.post('/auth/logout-all');
              location.reload();
            }}
          >
            Sign out everywhere
          </Button>
          <Button
            type="button"
            variant="ghost"
            onClick={async () => {
              await api.post('/auth/logout');
              location.reload();
            }}
          >
            Sign out
          </Button>
        </div>
      </form>
    </Section>
  );
}

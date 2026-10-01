import { Archive, Inbox, PenSquare, Send, Settings, ShieldAlert, Star, Trash2 } from 'lucide-react';
import type { Account, Counts, Folder } from './api';
import { cx } from './lib';
import { Button } from './ui';

const FOLDERS: { id: Folder; label: string; icon: typeof Inbox }[] = [
  { id: 'inbox', label: 'Inbox', icon: Inbox },
  { id: 'starred', label: 'Starred', icon: Star },
  { id: 'sent', label: 'Sent', icon: Send },
  { id: 'archive', label: 'Archive', icon: Archive },
  { id: 'spam', label: 'Spam', icon: ShieldAlert },
  { id: 'trash', label: 'Trash', icon: Trash2 },
];

export function Sidebar({
  accounts,
  counts,
  accountId,
  folder,
  onAccount,
  onFolder,
  onCompose,
  onSettings,
}: {
  accounts: Account[];
  counts: Counts;
  accountId: number | null;
  folder: Folder;
  onAccount: (id: number | null) => void;
  onFolder: (f: Folder) => void;
  onCompose: () => void;
  onSettings: () => void;
}) {
  const totalUnread = Object.values(counts.accounts).reduce((a, b) => a + b, 0);
  return (
    <nav aria-label="Mailboxes" className="flex h-full flex-col gap-5 overflow-y-auto scroll-thin px-3 py-4 safe-top">
      <div className="flex items-center gap-2.5 px-2">
        <img src="/icon.svg" alt="" className="size-7 rounded-lg" />
        <span className="text-[15px] font-semibold tracking-tight">Rebound</span>
      </div>

      <Button variant="primary" onClick={onCompose} className="w-full">
        <PenSquare className="size-4" aria-hidden />
        Compose
      </Button>

      <section>
        <h2 className="px-2 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-ink-3">Accounts</h2>
        <SideItem active={accountId === null} onClick={() => onAccount(null)} label="All accounts" count={totalUnread}>
          <span className="size-2.5 rounded-full bg-gradient-to-br from-ink-3 to-ink-2" />
        </SideItem>
        {accounts.map((a) => (
          <SideItem key={a.id} active={accountId === a.id} onClick={() => onAccount(a.id)} label={a.name} count={counts.accounts[a.id] ?? 0} warn={!!a.last_error}>
            <span className="size-2.5 rounded-full" style={{ background: a.color }} />
          </SideItem>
        ))}
      </section>

      <section>
        <h2 className="px-2 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-ink-3">Folders</h2>
        {FOLDERS.map((f) => (
          <SideItem
            key={f.id}
            active={folder === f.id}
            onClick={() => onFolder(f.id)}
            label={f.label}
            count={f.id === 'inbox' || f.id === 'spam' ? counts.folders[f.id] ?? 0 : 0}
            muted={f.id === 'spam'}
          >
            <f.icon className="size-4" aria-hidden />
          </SideItem>
        ))}
      </section>

      <div className="mt-auto">
        <SideItem active={false} onClick={onSettings} label="Settings" count={0}>
          <Settings className="size-4" aria-hidden />
        </SideItem>
      </div>
    </nav>
  );
}

function SideItem({
  active,
  onClick,
  label,
  count,
  muted,
  warn,
  children,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
  count: number;
  muted?: boolean;
  warn?: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      aria-current={active ? 'page' : undefined}
      className={cx(
        'flex h-9 w-full items-center gap-2.5 rounded-lg px-2 text-left text-sm transition-colors duration-150',
        active ? 'bg-surface-hover font-medium text-ink' : 'text-ink-2 hover:bg-surface-hover hover:text-ink',
      )}
    >
      <span className="flex size-4 items-center justify-center text-ink-3">{children}</span>
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {warn && <span className="size-1.5 rounded-full bg-danger" title="Sync error — see Settings" />}
      {count > 0 && (
        <span className={cx('text-xs tabular-nums', muted ? 'text-ink-3' : 'font-semibold text-accent')}>{count}</span>
      )}
    </button>
  );
}

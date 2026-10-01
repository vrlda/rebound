import { Menu, Paperclip, RefreshCw, Search, Star, X } from 'lucide-react';
import type { Account, Folder, MessageRow } from './api';
import { cx, displayName, formatListTime } from './lib';
import { IconButton, Spinner } from './ui';

const TITLES: Record<Folder, string> = {
  inbox: 'Inbox',
  starred: 'Starred',
  sent: 'Sent',
  archive: 'Archive',
  spam: 'Spam',
  trash: 'Trash',
};

export function MessageList({
  folder,
  account,
  accounts,
  messages,
  loading,
  hasMore,
  selectedId,
  query,
  onQuery,
  onSelect,
  onMore,
  onRefresh,
  refreshing,
  onMenu,
}: {
  folder: Folder;
  account: Account | null;
  accounts: Account[];
  messages: MessageRow[];
  loading: boolean;
  hasMore: boolean;
  selectedId: number | null;
  query: string;
  onQuery: (q: string) => void;
  onSelect: (m: MessageRow) => void;
  onMore: () => void;
  onRefresh: () => void;
  refreshing: boolean;
  onMenu?: () => void;
}) {
  const byId = new Map(accounts.map((a) => [a.id, a]));
  const showAccountDot = !account && accounts.length > 1;
  return (
    <section aria-label="Messages" className="flex h-full min-h-0 flex-col">
      <header className="flex items-center gap-1 border-b border-line px-3 py-2.5 safe-top">
        {onMenu && (
          <IconButton label="Open menu" onClick={onMenu}>
            <Menu className="size-5" />
          </IconButton>
        )}
        <div className="min-w-0 flex-1 px-1">
          <h1 className="truncate text-[15px] font-semibold tracking-tight">{query ? 'Search' : TITLES[folder]}</h1>
          <p className="truncate text-xs text-ink-3">{account ? account.name : 'All accounts'}</p>
        </div>
        <IconButton label="Refresh" onClick={onRefresh} disabled={refreshing}>
          <RefreshCw className={cx('size-4', refreshing && 'animate-spin')} />
        </IconButton>
      </header>

      <div className="border-b border-line px-3 py-2">
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-ink-3" aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            placeholder="Search mail"
            aria-label="Search mail"
            className="h-9 w-full rounded-lg bg-surface-2 pl-8 pr-8 text-sm outline-none placeholder:text-ink-3 focus:ring-2 focus:ring-accent-soft"
          />
          {query && (
            <button aria-label="Clear search" onClick={() => onQuery('')} className="absolute right-2 top-1/2 -translate-y-1/2 text-ink-3 hover:text-ink">
              <X className="size-4" />
            </button>
          )}
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto scroll-thin">
        {loading && messages.length === 0 ? (
          <div className="flex justify-center py-16">
            <Spinner />
          </div>
        ) : messages.length === 0 ? (
          <EmptyState folder={folder} searching={!!query} />
        ) : (
          <ul>
            {messages.map((m) => {
              const acc = byId.get(m.account_id);
              const who =
                m.direction === 'out'
                  ? `To: ${(m.to ?? []).map((t) => t.replace(/<.*>/, '').trim() || t).join(', ')}`
                  : displayName(m.from_name, m.from_addr);
              const unread = !m.is_read && m.direction === 'in';
              return (
                <li key={m.id}>
                  <button
                    onClick={() => onSelect(m)}
                    aria-current={selectedId === m.id ? 'true' : undefined}
                    className={cx(
                      'group relative flex w-full gap-3 border-b border-line px-4 py-3 text-left transition-colors duration-100',
                      selectedId === m.id ? 'bg-accent-soft' : 'hover:bg-surface-hover',
                    )}
                  >
                    <span className="mt-1.5 flex w-2 shrink-0 justify-center">
                      {unread && <span className="size-2 rounded-full bg-unread" aria-label="Unread" />}
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="flex items-baseline gap-2">
                        <span className={cx('min-w-0 flex-1 truncate text-sm', unread ? 'font-semibold text-ink' : 'text-ink-2')}>
                          {who}
                          {m.thread_count > 1 && <span className="ml-1.5 text-xs font-normal text-ink-3">{m.thread_count}</span>}
                        </span>
                        <span className={cx('shrink-0 text-xs tabular-nums', unread ? 'font-medium text-ink' : 'text-ink-3')}>
                          {formatListTime(m.received_at)}
                        </span>
                      </span>
                      <span className={cx('mt-0.5 flex items-center gap-1.5 text-[13px]', unread ? 'font-medium text-ink' : 'text-ink-2')}>
                        {showAccountDot && acc && (
                          <span className="size-1.5 shrink-0 rounded-full" style={{ background: acc.color }} title={acc.name} />
                        )}
                        <span className="truncate">{m.subject || '(no subject)'}</span>
                        {m.is_starred && <Star className="size-3.5 shrink-0 fill-current text-accent" aria-label="Starred" />}
                        {m.attachments > 0 && <Paperclip className="size-3.5 shrink-0 text-ink-3" aria-label="Has attachments" />}
                      </span>
                      <span className="mt-0.5 line-clamp-1 text-[13px] text-ink-3">{m.spam_reason && folder === 'spam' ? `Spam: ${m.spam_reason} · ` : ''}{m.snippet}</span>
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        {hasMore && messages.length > 0 && (
          <div className="flex justify-center py-4">
            <button onClick={onMore} className="text-[13px] font-medium text-accent hover:underline" disabled={loading}>
              {loading ? 'Loading…' : 'Load older'}
            </button>
          </div>
        )}
      </div>
    </section>
  );
}

function EmptyState({ folder, searching }: { folder: Folder; searching: boolean }) {
  const text = searching
    ? 'Nothing matches that search.'
    : folder === 'inbox'
      ? 'Inbox zero. New mail syncs every 45 seconds.'
      : folder === 'spam'
        ? 'No spam. Anything you mark as spam lands here.'
        : `No messages in ${TITLES[folder]}.`;
  return <p className="px-6 py-16 text-center text-sm text-ink-3">{text}</p>;
}

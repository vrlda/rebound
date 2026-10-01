import { useCallback, useEffect, useRef, useState } from 'react';
import { PenSquare } from 'lucide-react';
import { api, ApiError, type Account, type Counts, type Folder, type MessageDetail, type MessageRow } from './api';
import { Composer, type ComposeInit } from './Composer';
import { MessageList } from './MessageList';
import { Settings } from './Settings';
import { Sidebar } from './Sidebar';
import { ThreadView, type ComposeMode } from './ThreadView';
import { cx } from './lib';
import { useMediaQuery, useToast } from './ui';

const POLL_MS = 30_000;

function messageIdFromPath(): number | null {
  const m = window.location.pathname.match(/^\/m\/(\d+)/);
  return m ? Number(m[1]) : null;
}

export function Mail() {
  const toast = useToast();
  const desktop = useMediaQuery('(min-width: 1024px)');
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [counts, setCounts] = useState<Counts>({ folders: {}, accounts: {} });
  const [accountId, setAccountId] = useState<number | null>(null);
  const [folder, setFolder] = useState<Folder>('inbox');
  const [query, setQuery] = useState('');
  const [debouncedQuery, setDebouncedQuery] = useState('');
  const [messages, setMessages] = useState<MessageRow[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loadingList, setLoadingList] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [selectedId, setSelectedId] = useState<number | null>(messageIdFromPath);
  const [thread, setThread] = useState<MessageDetail[] | null>(null);
  const [loadingThread, setLoadingThread] = useState(false);
  const [compose, setCompose] = useState<ComposeInit | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [drawer, setDrawer] = useState(false);
  const listKey = useRef('');

  const loadMeta = useCallback(async () => {
    const [accs, cts] = await Promise.all([api.get<Account[]>('/accounts'), api.get<Counts>('/counts')]);
    setAccounts(accs);
    setCounts(cts);
    if (accs.length === 0) setSettingsOpen(true);
  }, []);

  const loadList = useCallback(
    async (opts: { append?: boolean; silent?: boolean } = {}) => {
      const params = new URLSearchParams();
      if (debouncedQuery) params.set('q', debouncedQuery);
      // Searching from Inbox searches every folder except Trash.
      if (!(debouncedQuery && folder === 'inbox')) params.set('folder', folder);
      if (accountId) params.set('account', String(accountId));
      if (opts.append && messages.length) params.set('before', String(messages[messages.length - 1].received_at));
      const key = `${folder}|${accountId}|${debouncedQuery}`;
      listKey.current = key;
      if (!opts.silent) setLoadingList(true);
      try {
        const res = await api.get<{ messages: MessageRow[]; has_more: boolean }>(`/messages?${params}`);
        if (listKey.current !== key) return; // a newer request superseded this one
        setMessages((prev) => (opts.append ? [...prev, ...res.messages] : res.messages));
        setHasMore(res.has_more);
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 401)) toast('Could not load messages', 'error');
      } finally {
        if (listKey.current === key) setLoadingList(false);
      }
    },
    [folder, accountId, debouncedQuery, messages, toast],
  );

  useEffect(() => {
    const t = setTimeout(() => setDebouncedQuery(query.trim()), 250);
    return () => clearTimeout(t);
  }, [query]);

  useEffect(() => {
    loadMeta().catch(() => {});
  }, [loadMeta]);

  useEffect(() => {
    loadList();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [folder, accountId, debouncedQuery]);

  // Background refresh: poll + on focus.
  const refresh = useCallback(async () => {
    await Promise.all([loadMeta(), loadList({ silent: true })]).catch(() => {});
  }, [loadMeta, loadList]);
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  useEffect(() => {
    const id = setInterval(() => document.visibilityState === 'visible' && refreshRef.current(), POLL_MS);
    const onFocus = () => refreshRef.current();
    window.addEventListener('focus', onFocus);
    return () => {
      clearInterval(id);
      window.removeEventListener('focus', onFocus);
    };
  }, []);

  // Thread loading follows the selection.
  useEffect(() => {
    if (!selectedId) {
      setThread(null);
      return;
    }
    let cancelled = false;
    setLoadingThread(true);
    api
      .get<{ messages: MessageDetail[] }>(`/messages/${selectedId}`)
      .then((r) => {
        if (cancelled) return;
        setThread(r.messages);
        setMessages((prev) => prev.map((m) => (r.messages.some((t) => t.thread_id === m.thread_id) ? { ...m, is_read: true } : m)));
        api.get<Counts>('/counts').then(setCounts).catch(() => {});
      })
      .catch(() => !cancelled && setThread([]))
      .finally(() => !cancelled && setLoadingThread(false));
    return () => {
      cancelled = true;
    };
  }, [selectedId]);

  // URL sync so push notifications can deep-link to /m/:id.
  const select = useCallback((id: number | null) => {
    setSelectedId(id);
    const path = id ? `/m/${id}` : '/';
    if (window.location.pathname !== path) history.pushState(null, '', path);
  }, []);
  useEffect(() => {
    const onPop = () => setSelectedId(messageIdFromPath());
    window.addEventListener('popstate', onPop);
    const onSW = (e: MessageEvent) => {
      if (e.data?.type === 'open' && typeof e.data.url === 'string') {
        const m = e.data.url.match(/\/m\/(\d+)/);
        if (m) select(Number(m[1]));
        refreshRef.current();
      }
    };
    navigator.serviceWorker?.addEventListener('message', onSW);
    return () => {
      window.removeEventListener('popstate', onPop);
      navigator.serviceWorker?.removeEventListener('message', onSW);
    };
  }, [select]);

  const advanceAfterRemoval = useCallback(
    (threadId: number) => {
      const idx = messages.findIndex((m) => m.thread_id === threadId);
      const remaining = messages.filter((m) => m.thread_id !== threadId);
      setMessages(remaining);
      const next = desktop ? remaining[Math.min(idx, remaining.length - 1)] : undefined;
      select(next ? next.id : null);
    },
    [messages, desktop, select],
  );

  const move = useCallback(
    async (target: string) => {
      if (!selectedId || !thread?.length) return;
      const threadId = thread[0].thread_id;
      try {
        await api.patch(`/messages/${selectedId}`, { folder: target });
        const labels: Record<string, string> = { archive: 'Archived', trash: 'Moved to Trash', spam: 'Marked as spam — sender blocked', inbox: 'Moved to Inbox' };
        toast(labels[target] ?? 'Moved');
        advanceAfterRemoval(threadId);
        loadMeta().catch(() => {});
      } catch (err) {
        toast(err instanceof ApiError ? err.message : 'Action failed', 'error');
      }
    },
    [selectedId, thread, toast, advanceAfterRemoval, loadMeta],
  );

  const star = useCallback(async (m: MessageDetail) => {
    const next = !m.is_starred;
    setThread((t) => t?.map((x) => (x.id === m.id ? { ...x, is_starred: next } : x)) ?? null);
    setMessages((prev) => prev.map((x) => (x.id === m.id ? { ...x, is_starred: next } : x)));
    await api.patch(`/messages/${m.id}`, { is_starred: next }).catch(() => {});
  }, []);

  const markUnread = useCallback(async () => {
    if (!selectedId || !thread?.length) return;
    const threadId = thread[0].thread_id;
    await api.patch(`/messages/${selectedId}`, { is_read: false }).catch(() => {});
    setMessages((prev) => prev.map((m) => (m.thread_id === threadId ? { ...m, is_read: false } : m)));
    select(null);
    loadMeta().catch(() => {});
  }, [selectedId, thread, select, loadMeta]);

  const deleteForever = useCallback(async () => {
    if (!selectedId || !thread?.length) return;
    if (!confirm('Delete this conversation permanently? This cannot be undone.')) return;
    await api.del(`/messages/${selectedId}`);
    toast('Deleted');
    advanceAfterRemoval(thread[0].thread_id);
  }, [selectedId, thread, toast, advanceAfterRemoval]);

  const openCompose = useCallback((mode: ComposeMode | 'new', source?: MessageDetail) => {
    setCompose({ mode, source, accountId });
  }, [accountId]);

  const syncNow = useCallback(async () => {
    setRefreshing(true);
    try {
      const targets = accountId ? accounts.filter((a) => a.id === accountId) : accounts;
      await Promise.all(targets.map((a) => api.post(`/accounts/${a.id}/sync`)));
      await refresh();
    } catch (err) {
      toast(err instanceof ApiError ? err.message : 'Sync failed', 'error');
    } finally {
      setRefreshing(false);
    }
  }, [accountId, accounts, refresh, toast]);

  // Keyboard shortcuts (ignored while typing).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (compose || e.metaKey || e.ctrlKey || e.altKey || el.closest('input, textarea, select, [contenteditable]')) return;
      const idx = messages.findIndex((m) => m.id === selectedId);
      switch (e.key) {
        case 'c':
          e.preventDefault();
          openCompose('new');
          break;
        case 'j':
          if (messages[idx + 1]) select(messages[idx + 1].id);
          break;
        case 'k':
          if (idx > 0) select(messages[idx - 1].id);
          break;
        case 'e':
          move('archive');
          break;
        case '#':
          move('trash');
          break;
        case 'r':
          if (thread?.length) {
            e.preventDefault();
            openCompose('reply', [...thread].reverse().find((m) => m.direction === 'in') ?? thread[thread.length - 1]);
          }
          break;
        case 'Escape':
          select(null);
          break;
        case '/':
          e.preventDefault();
          document.querySelector<HTMLInputElement>('input[type=search]')?.focus();
          break;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [compose, messages, selectedId, thread, select, move, openCompose]);

  // Title badge.
  useEffect(() => {
    const unread = Object.values(counts.accounts).reduce((a, b) => a + b, 0);
    document.title = unread ? `(${unread}) Rebound` : 'Rebound';
    (navigator as Navigator & { setAppBadge?: (n: number) => Promise<void> }).setAppBadge?.(unread).catch(() => {});
  }, [counts]);

  const account = accounts.find((a) => a.id === accountId) ?? null;

  const sidebar = (
    <Sidebar
      accounts={accounts}
      counts={counts}
      accountId={accountId}
      folder={folder}
      onAccount={(id) => {
        setAccountId(id);
        setSettingsOpen(false);
        setDrawer(false);
        select(null);
      }}
      onFolder={(f) => {
        setFolder(f);
        setQuery('');
        setSettingsOpen(false);
        setDrawer(false);
        select(null);
      }}
      onCompose={() => {
        setDrawer(false);
        openCompose('new');
      }}
      onSettings={() => {
        setSettingsOpen(true);
        setDrawer(false);
      }}
    />
  );

  const list = (
    <MessageList
      folder={folder}
      account={account}
      accounts={accounts}
      messages={messages}
      loading={loadingList}
      hasMore={hasMore}
      selectedId={selectedId}
      query={query}
      onQuery={setQuery}
      onSelect={(m) => select(m.id)}
      onMore={() => loadList({ append: true })}
      onRefresh={syncNow}
      refreshing={refreshing}
      onMenu={desktop ? undefined : () => setDrawer(true)}
    />
  );

  const threadView = (
    <ThreadView
      messages={thread}
      loading={loadingThread}
      accounts={accounts}
      onBack={desktop ? undefined : () => select(null)}
      onMove={move}
      onStar={star}
      onMarkUnread={markUnread}
      onDeleteForever={deleteForever}
      onCompose={(mode, m) => openCompose(mode, m)}
    />
  );

  const settings = (
    <Settings
      accounts={accounts}
      onBack={() => setSettingsOpen(false)}
      onChanged={() => {
        loadMeta().catch(() => {});
        loadList({ silent: true });
      }}
    />
  );

  return (
    <div className="h-[100dvh] overflow-hidden bg-canvas text-ink">
      {desktop ? (
        <div className="grid h-full grid-cols-[232px_minmax(320px,400px)_1fr]">
          <aside className="border-r border-line bg-canvas">{sidebar}</aside>
          {settingsOpen ? (
            <div className="col-span-2 h-full min-h-0 overflow-hidden bg-surface">{settings}</div>
          ) : (
            <>
              <div className="min-h-0 border-r border-line bg-surface">{list}</div>
              <div className="min-h-0 bg-canvas">{threadView}</div>
            </>
          )}
        </div>
      ) : (
        <div className="relative h-full bg-surface">
          {settingsOpen ? settings : selectedId ? threadView : list}
          {!settingsOpen && !selectedId && (
            <button
              onClick={() => openCompose('new')}
              aria-label="Compose"
              className="fixed bottom-5 right-5 z-30 flex size-14 items-center justify-center rounded-2xl bg-accent text-accent-ink shadow-card transition-transform active:scale-95"
              style={{ marginBottom: 'env(safe-area-inset-bottom)' }}
            >
              <PenSquare className="size-5" />
            </button>
          )}
          <div
            className={cx('fixed inset-0 z-40 bg-black/40 transition-opacity duration-200', drawer ? 'opacity-100' : 'pointer-events-none opacity-0')}
            onClick={() => setDrawer(false)}
            aria-hidden
          />
          <aside
            className={cx(
              'fixed inset-y-0 left-0 z-50 w-72 bg-canvas shadow-card transition-transform duration-200 ease-out',
              drawer ? 'translate-x-0' : '-translate-x-full',
            )}
            aria-hidden={!drawer}
          >
            {sidebar}
          </aside>
        </div>
      )}
      {compose && (
        <Composer
          key={`${compose.mode}-${compose.source?.id ?? 'new'}`}
          init={compose}
          accounts={accounts}
          onClose={() => setCompose(null)}
          onSent={() => {
            if (folder === 'sent') loadList({ silent: true });
            if (selectedId) {
              api.get<{ messages: MessageDetail[] }>(`/messages/${selectedId}`).then((r) => setThread(r.messages)).catch(() => {});
            }
          }}
        />
      )}
    </div>
  );
}

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Archive,
  ArrowLeft,
  Download,
  Forward,
  ImageOff,
  Inbox,
  MailOpen,
  Paperclip,
  Reply,
  ReplyAll,
  ShieldAlert,
  ShieldCheck,
  Star,
  Trash2,
} from 'lucide-react';
import type { Account, MessageDetail } from './api';
import { cx, displayName, escapeHtml, formatBytes, formatFullTime, formatListTime, initials } from './lib';
import { Avatar, Button, IconButton, Spinner } from './ui';

export type ComposeMode = 'reply' | 'replyAll' | 'forward';

export function ThreadView({
  messages,
  loading,
  accounts,
  onBack,
  onMove,
  onStar,
  onMarkUnread,
  onDeleteForever,
  onCompose,
}: {
  messages: MessageDetail[] | null;
  loading: boolean;
  accounts: Account[];
  onBack?: () => void;
  onMove: (folder: string) => void;
  onStar: (m: MessageDetail) => void;
  onMarkUnread: () => void;
  onDeleteForever: () => void;
  onCompose: (mode: ComposeMode, m: MessageDetail) => void;
}) {
  if (loading && !messages) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (!messages || messages.length === 0) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-2 text-ink-3">
        <MailOpen className="size-8" aria-hidden />
        <p className="text-sm">Select a conversation</p>
      </div>
    );
  }
  const last = messages[messages.length - 1];
  const lastInbound = [...messages].reverse().find((m) => m.direction === 'in') ?? last;
  const folder = lastInbound.folder;
  const account = accounts.find((a) => a.id === last.account_id);

  return (
    <article className="flex h-full min-h-0 flex-col">
      <header className="flex items-center gap-0.5 border-b border-line px-2 py-2 safe-top">
        {onBack && (
          <IconButton label="Back" onClick={onBack}>
            <ArrowLeft className="size-5" />
          </IconButton>
        )}
        <div className="flex-1" />
        {folder === 'spam' ? (
          <Button size="sm" variant="ghost" onClick={() => onMove('inbox')}>
            <ShieldCheck className="size-4" /> Not spam
          </Button>
        ) : folder === 'trash' || folder === 'archive' ? (
          <IconButton label="Move to Inbox" onClick={() => onMove('inbox')}>
            <Inbox className="size-4" />
          </IconButton>
        ) : (
          <IconButton label="Archive" onClick={() => onMove('archive')}>
            <Archive className="size-4" />
          </IconButton>
        )}
        {folder !== 'spam' && (
          <IconButton label="Mark as spam" onClick={() => onMove('spam')}>
            <ShieldAlert className="size-4" />
          </IconButton>
        )}
        {folder === 'trash' ? (
          <IconButton label="Delete forever" onClick={onDeleteForever} className="hover:text-danger">
            <Trash2 className="size-4" />
          </IconButton>
        ) : (
          <IconButton label="Move to Trash" onClick={() => onMove('trash')}>
            <Trash2 className="size-4" />
          </IconButton>
        )}
        <IconButton label="Mark as unread" onClick={onMarkUnread}>
          <MailOpen className="size-4" />
        </IconButton>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto scroll-thin">
        <div className="mx-auto max-w-3xl px-4 pb-24 pt-5 md:px-8">
          <div className="mb-5 flex items-start gap-3">
            <h1 className="min-w-0 flex-1 text-xl font-semibold leading-snug tracking-tight text-balance">{last.subject || '(no subject)'}</h1>
            {account && (
              <span className="mt-1 inline-flex shrink-0 items-center gap-1.5 rounded-full border border-line px-2 py-0.5 text-xs text-ink-2">
                <span className="size-1.5 rounded-full" style={{ background: account.color }} />
                {account.name}
              </span>
            )}
          </div>
          {lastInbound.spam_reason && folder === 'spam' && (
            <p className="mb-4 rounded-lg bg-danger-soft px-3 py-2 text-[13px] text-danger">Filtered as spam ({lastInbound.spam_reason}).</p>
          )}
          <div className="space-y-3">
            {messages.map((m, i) => (
              <MessageCard
                key={m.id}
                m={m}
                color={accounts.find((a) => a.id === m.account_id)?.color}
                defaultOpen={i === messages.length - 1 || !m.is_read}
                onStar={() => onStar(m)}
              />
            ))}
          </div>
          <div className="mt-5 flex flex-wrap gap-2">
            <Button onClick={() => onCompose('reply', lastInbound)}>
              <Reply className="size-4" /> Reply
            </Button>
            <Button onClick={() => onCompose('replyAll', lastInbound)}>
              <ReplyAll className="size-4" /> Reply all
            </Button>
            <Button onClick={() => onCompose('forward', last)}>
              <Forward className="size-4" /> Forward
            </Button>
          </div>
        </div>
      </div>
    </article>
  );
}

function MessageCard({ m, color, defaultOpen, onStar }: { m: MessageDetail; color?: string; defaultOpen: boolean; onStar: () => void }) {
  const [open, setOpen] = useState(defaultOpen);
  const who = displayName(m.from_name, m.from_addr);
  const recipients = [...(m.to ?? []), ...(m.cc ?? [])].join(', ');
  return (
    <div className="rounded-xl border border-line bg-surface shadow-card">
      <button className="flex w-full items-start gap-3 px-4 py-3 text-left" onClick={() => setOpen((o) => !o)} aria-expanded={open}>
        <Avatar text={initials(m.from_name, m.from_addr)} color={m.direction === 'out' ? color : undefined} />
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline gap-2">
            <span className="max-w-[60%] shrink-0 truncate text-sm font-semibold">{m.direction === 'out' ? 'You' : who}</span>
            {m.direction === 'in' && m.from_name && <span className="hidden min-w-0 truncate text-xs text-ink-3 lg:inline">{m.from_addr}</span>}
            <span className="ml-auto hidden shrink-0 text-xs text-ink-3 sm:inline">{formatFullTime(m.received_at)}</span>
            <span className="ml-auto shrink-0 text-xs text-ink-3 sm:hidden">{formatListTime(m.received_at)}</span>
          </span>
          <span className="mt-0.5 block truncate text-xs text-ink-3">{open ? `to ${recipients || 'undisclosed recipients'}` : m.snippet}</span>
        </span>
        <span
          role="button"
          tabIndex={0}
          aria-label={m.is_starred ? 'Unstar' : 'Star'}
          onClick={(e) => {
            e.stopPropagation();
            onStar();
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.stopPropagation();
              onStar();
            }
          }}
          className="-m-1 ml-1 shrink-0 p-1 text-ink-3 hover:text-accent"
        >
          <Star className={cx('size-4', m.is_starred && 'fill-current text-accent')} />
        </span>
      </button>
      {open && (
        <div className="animate-fade border-t border-line px-4 pb-4 pt-3">
          <EmailBody m={m} />
          {m.files.length > 0 && (
            <ul className="mt-4 flex flex-wrap gap-2">
              {m.files.map((f) => (
                <li key={f.id}>
                  <a
                    href={`/api/attachments/${f.id}`}
                    className="flex max-w-64 items-center gap-2 rounded-lg border border-line bg-surface-2 px-3 py-2 text-[13px] hover:bg-surface-hover"
                  >
                    <Paperclip className="size-4 shrink-0 text-ink-3" aria-hidden />
                    <span className="min-w-0 flex-1 truncate">{f.filename}</span>
                    <span className="shrink-0 text-xs text-ink-3">{formatBytes(f.size)}</span>
                    <Download className="size-3.5 shrink-0 text-ink-3" aria-hidden />
                  </a>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

const isDark = () => window.matchMedia('(prefers-color-scheme: dark)').matches;

/**
 * Renders untrusted email HTML in a sandboxed iframe: no scripts, no forms,
 * remote images blocked until the user opts in (tracking pixels), links open
 * in a new tab without referrer.
 */
function EmailBody({ m }: { m: MessageDetail }) {
  const ref = useRef<HTMLIFrameElement>(null);
  const [height, setHeight] = useState(60);
  const hasRemote = useMemo(() => /<img[^>]+src=["']?https?:/i.test(m.html) || /url\(["']?https?:/i.test(m.html), [m.html]);
  const [loadImages, setLoadImages] = useState(m.direction === 'out');
  const ownColors = useMemo(() => /(^|[\s;"'{])color\s*:|<font[^>]+color|bgcolor|background(-color)?\s*:/i.test(m.html), [m.html]);

  const srcDoc = useMemo(() => {
    const origin = window.location.origin;
    const imgSrc = loadImages ? `${origin} data: https:` : `${origin} data:`;
    let body = m.html;
    for (const f of m.files) {
      if (f.content_id) body = body.split(`cid:${f.content_id}`).join(`/api/attachments/${f.id}?inline=1`);
    }
    if (!body.trim()) {
      body = `<pre class="plain">${escapeHtml(m.text || '(empty message)')}</pre>`;
    }
    // Emails that set their own colors (e.g. Gmail's dark-grey text) assume a
    // white page; forcing a dark theme on them makes text unreadable. Render
    // those on a white "paper" surface and only theme uncolored mail.
    const dark = isDark() && !ownColors;
    return `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${imgSrc}; style-src 'unsafe-inline'; font-src data: https:">
<meta name="referrer" content="no-referrer"><base target="_blank">
<style>
html,body{margin:0;padding:0}
body{font:14px/1.55 Geist,ui-sans-serif,system-ui,sans-serif;color:${dark ? '#eceef1' : '#16171a'};background:${ownColors ? '#ffffff' : 'transparent'};padding:${ownColors && isDark() ? '14px' : '0'};overflow-wrap:anywhere}
a{color:${dark ? '#a5a0ff' : '#4f46e5'}}
img{max-width:100%;height:auto}
table{max-width:100%}
blockquote{margin:0 0 0 .6em;padding-left:.8em;border-left:2px solid ${dark ? '#333842' : '#d3d3cc'};color:${dark ? '#b3b7bf' : '#4a4c52'}}
pre.plain{white-space:pre-wrap;font:inherit;margin:0}
</style></head><body>${body}</body></html>`;
  }, [m.html, m.text, m.files, loadImages, ownColors]);

  useEffect(() => {
    const frame = ref.current;
    if (!frame) return;
    let ro: ResizeObserver | undefined;
    const measure = () => {
      const doc = frame.contentDocument;
      // Measure the body, not documentElement: the latter never shrinks below
      // the iframe's current height, leaving blank space under short emails.
      if (doc?.body) setHeight(Math.max(24, Math.ceil(doc.body.getBoundingClientRect().height) + 2));
    };
    const onLoad = () => {
      measure();
      const doc = frame.contentDocument;
      if (doc?.body) {
        ro = new ResizeObserver(measure);
        ro.observe(doc.body);
      }
    };
    frame.addEventListener('load', onLoad);
    return () => {
      frame.removeEventListener('load', onLoad);
      ro?.disconnect();
    };
  }, [srcDoc]);

  return (
    <div>
      {hasRemote && !loadImages && (
        <button onClick={() => setLoadImages(true)} className="mb-3 flex items-center gap-1.5 text-xs font-medium text-accent hover:underline">
          <ImageOff className="size-3.5" aria-hidden /> Remote images blocked for privacy — show images
        </button>
      )}
      <iframe
        ref={ref}
        title="Email content"
        sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
        srcDoc={srcDoc}
        style={{ height }}
        className={cx('block w-full border-0', ownColors && isDark() ? 'rounded-lg bg-white' : 'bg-transparent')}
      />
    </div>
  );
}

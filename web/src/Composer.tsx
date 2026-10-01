import { useEffect, useMemo, useRef, useState } from 'react';
import { Minus, Paperclip, X } from 'lucide-react';
import { api, ApiError, type Account, type MessageDetail, type SendAttachment } from './api';
import type { ComposeMode } from './ThreadView';
import { bareAddress, cx, escapeHtml, fileToBase64, formatBytes, formatFullTime, replySubject, splitRecipients, textToHtml } from './lib';
import { Button, ErrorText, IconButton, useToast } from './ui';

export interface ComposeInit {
  mode: 'new' | ComposeMode;
  source?: MessageDetail;
  accountId?: number | null;
}

const MAX_ATTACH_BYTES = 30 * 1024 * 1024;
const DRAFT_KEY = 'inbox:draft:new';

function quoteBlock(m: MessageDetail) {
  const who = m.from_name ? `${m.from_name} <${m.from_addr}>` : m.from_addr;
  const header = `On ${formatFullTime(m.received_at)}, ${who} wrote:`;
  const inner = m.html.trim() ? m.html : textToHtml(m.text);
  return {
    html: `<br><br><div>${escapeHtml(header)}</div><blockquote style="margin:0 0 0 .6em;padding-left:.8em;border-left:2px solid #ccc">${inner}</blockquote>`,
    text: `\n\n${header}\n${(m.text || '').split('\n').map((l) => `> ${l}`).join('\n')}`,
  };
}

function forwardBlock(m: MessageDetail) {
  const lines = [
    '---------- Forwarded message ---------',
    `From: ${m.from_name ? `${m.from_name} <${m.from_addr}>` : m.from_addr}`,
    `Date: ${formatFullTime(m.received_at)}`,
    `Subject: ${m.subject}`,
    `To: ${(m.to ?? []).join(', ')}`,
  ];
  return {
    html: `<br><br><div>${lines.map(escapeHtml).join('<br>')}</div><br>${m.html.trim() ? m.html : textToHtml(m.text)}`,
    text: `\n\n${lines.join('\n')}\n\n${m.text}`,
  };
}

export function Composer({ init, accounts, onClose, onSent }: { init: ComposeInit; accounts: Account[]; onClose: () => void; onSent: () => void }) {
  const toast = useToast();
  const src = init.source;
  const firstAccount = src?.account_id ?? init.accountId ?? accounts[0]?.id;
  const [accountId, setAccountId] = useState<number | undefined>(firstAccount ?? undefined);
  const account = accounts.find((a) => a.id === accountId);

  const initial = useMemo(() => {
    if (!src) {
      try {
        const saved = JSON.parse(localStorage.getItem(DRAFT_KEY) || 'null');
        if (saved) return saved as { to: string; cc: string; subject: string; body: string };
      } catch {
        /* ignore corrupted draft */
      }
      return { to: '', cc: '', subject: '', body: '' };
    }
    const own = new Set((account?.identities ?? []).map((x) => x.toLowerCase()));
    if (init.mode === 'forward') return { to: '', cc: '', subject: replySubject(src.subject, 'Fwd'), body: '' };
    const replyTarget = src.direction === 'out' ? (src.to ?? []) : src.reply_to?.length ? src.reply_to : [src.from_addr];
    let cc: string[] = [];
    if (init.mode === 'replyAll') {
      const seen = new Set(replyTarget.map(bareAddress));
      cc = [...(src.to ?? []), ...(src.cc ?? [])].filter((a) => {
        const b = bareAddress(a);
        if (own.has(b) || seen.has(b)) return false;
        seen.add(b);
        return true;
      });
    }
    return { to: replyTarget.join(', '), cc: cc.join(', '), subject: replySubject(src.subject, 'Re'), body: '' };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pickFrom = (acc?: Account) => {
    if (!acc) return '';
    if (src) {
      const addressed = [...(src.to ?? []), ...(src.cc ?? [])].map(bareAddress);
      const hit = acc.identities.find((i) => addressed.includes(i.toLowerCase()));
      if (hit) return hit;
    }
    return acc.default_from || acc.identities[0] || '';
  };

  const [from, setFrom] = useState(() => pickFrom(account));
  const [fromName, setFromName] = useState(() => localStorage.getItem('inbox:fromName') || '');
  const [to, setTo] = useState(initial.to);
  const [cc, setCc] = useState(initial.cc);
  const [bcc, setBcc] = useState('');
  const [showCc, setShowCc] = useState(!!initial.cc);
  const [subject, setSubject] = useState(initial.subject);
  const [body, setBody] = useState(initial.body);
  const [files, setFiles] = useState<(SendAttachment & { size: number })[]>([]);
  const [error, setError] = useState('');
  const [sending, setSending] = useState(false);
  const [minimized, setMinimized] = useState(false);
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (src && init.mode !== 'forward') bodyRef.current?.focus({ preventScroll: true });
  }, [src, init.mode]);

  // Autosave new-message drafts so a closed tab doesn't lose work.
  useEffect(() => {
    if (src) return;
    const t = setTimeout(() => localStorage.setItem(DRAFT_KEY, JSON.stringify({ to, cc, subject, body })), 400);
    return () => clearTimeout(t);
  }, [src, to, cc, subject, body]);

  // Forward carries the original attachments.
  useEffect(() => {
    if (init.mode !== 'forward' || !src?.files.length) return;
    let cancelled = false;
    Promise.all(
      src.files.map(async (f) => {
        const blob = await fetch(`/api/attachments/${f.id}`, { credentials: 'same-origin' }).then((r) => r.blob());
        return { filename: f.filename, content_type: f.content_type, content: await fileToBase64(blob), size: f.size };
      }),
    )
      .then((list) => !cancelled && setFiles(list))
      .catch(() => toast('Could not attach the original files', 'error'));
    return () => {
      cancelled = true;
    };
  }, [init.mode, src, toast]);

  const identities = account?.identities ?? [];

  async function addFiles(list: FileList | null) {
    if (!list) return;
    const next = [...files];
    for (const f of Array.from(list)) {
      next.push({ filename: f.name, content_type: f.type || 'application/octet-stream', content: await fileToBase64(f), size: f.size });
    }
    if (next.reduce((s, f) => s + f.size, 0) > MAX_ATTACH_BYTES) {
      setError('Attachments are limited to 30 MB in total.');
      return;
    }
    setFiles(next);
  }

  async function send() {
    if (!account) return setError('Connect a Resend account first (Settings).');
    if (!from.includes('@')) return setError('Choose or type a From address.');
    const toList = splitRecipients(to);
    if (toList.length + splitRecipients(cc).length + splitRecipients(bcc).length === 0) return setError('Add a recipient.');
    setSending(true);
    setError('');
    const signature = account.signature.trim();
    const textBody = body + (signature ? `\n\n-- \n${signature}` : '');
    let html = `<div style="font-family:system-ui,sans-serif;font-size:14px;line-height:1.55">${textToHtml(textBody)}</div>`;
    let text = textBody;
    if (src && init.mode !== 'forward') {
      const q = quoteBlock(src);
      html += q.html;
      text += q.text;
    } else if (src && init.mode === 'forward') {
      const f = forwardBlock(src);
      html += f.html;
      text += f.text;
    }
    const fromHeader = fromName.trim() ? `${fromName.trim().replace(/[<>"]/g, '')} <${from.trim()}>` : from.trim();
    try {
      const res = await api.post<{ warning?: string }>('/send', {
        account_id: account.id,
        from: fromHeader,
        to: toList,
        cc: splitRecipients(cc),
        bcc: splitRecipients(bcc),
        subject,
        text,
        html,
        reply_to_id: src && init.mode !== 'forward' ? src.id : undefined,
        attachments: files.map(({ filename, content, content_type }) => ({ filename, content, content_type })),
      });
      localStorage.setItem('inbox:fromName', fromName.trim());
      if (!src) localStorage.removeItem(DRAFT_KEY);
      toast(res.warning || 'Sent');
      onSent();
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Sending failed');
    } finally {
      setSending(false);
    }
  }

  function discard() {
    if (!src) localStorage.removeItem(DRAFT_KEY);
    onClose();
  }

  const title = init.mode === 'new' ? 'New message' : init.mode === 'forward' ? 'Forward' : 'Reply';

  return (
    <div
      role="dialog"
      aria-label={title}
      className={cx(
        'animate-rise fixed z-40 flex flex-col overflow-hidden border border-line bg-surface shadow-card',
        'inset-0 lg:inset-auto lg:bottom-4 lg:right-4 lg:w-[620px] lg:rounded-2xl',
        minimized ? 'lg:h-12' : 'lg:h-[min(720px,calc(100dvh-2rem))]',
      )}
      onKeyDown={(e) => {
        if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
          e.preventDefault();
          send();
        }
      }}
    >
      <header className="flex min-h-12 shrink-0 items-center gap-1 border-b border-line pb-1.5 pl-4 pr-2 safe-top">
        <h2 className="flex-1 truncate text-sm font-semibold">{subject || title}</h2>
        <IconButton label={minimized ? 'Expand' : 'Minimize'} onClick={() => setMinimized((v) => !v)} className="hidden lg:inline-flex">
          <Minus className="size-4" />
        </IconButton>
        <IconButton label="Close" onClick={onClose}>
          <X className="size-4" />
        </IconButton>
      </header>
      {!minimized && (
        <>
          <div className="shrink-0 divide-y divide-line border-b border-line text-sm">
            <Row label="From">
              <select
                aria-label="Account"
                value={accountId}
                onChange={(e) => {
                  const next = accounts.find((a) => a.id === Number(e.target.value));
                  setAccountId(next?.id);
                  setFrom(pickFrom(next));
                }}
                className="max-w-32 truncate rounded-md bg-surface-2 px-2 py-1 text-[13px] outline-none"
              >
                {accounts.map((a) => (
                  <option key={a.id} value={a.id}>
                    {a.name}
                  </option>
                ))}
              </select>
              <input
                aria-label="Your name"
                value={fromName}
                onChange={(e) => setFromName(e.target.value)}
                placeholder="Your name"
                className="w-28 min-w-0 bg-transparent outline-none placeholder:text-ink-3"
              />
              <input
                aria-label="From address"
                list="identities"
                value={from}
                onChange={(e) => setFrom(e.target.value)}
                placeholder="you@yourdomain.com"
                className="min-w-0 flex-1 bg-transparent outline-none placeholder:text-ink-3"
              />
              <datalist id="identities">
                {identities.map((i) => (
                  <option key={i} value={i} />
                ))}
              </datalist>
            </Row>
            <Row label="To">
              <input aria-label="To" value={to} onChange={(e) => setTo(e.target.value)} autoFocus={!src || init.mode === 'forward'} className="min-w-0 flex-1 bg-transparent outline-none" />
              {!showCc && (
                <button onClick={() => setShowCc(true)} className="text-xs font-medium text-ink-3 hover:text-ink">
                  Cc/Bcc
                </button>
              )}
            </Row>
            {showCc && (
              <>
                <Row label="Cc">
                  <input aria-label="Cc" value={cc} onChange={(e) => setCc(e.target.value)} className="min-w-0 flex-1 bg-transparent outline-none" />
                </Row>
                <Row label="Bcc">
                  <input aria-label="Bcc" value={bcc} onChange={(e) => setBcc(e.target.value)} className="min-w-0 flex-1 bg-transparent outline-none" />
                </Row>
              </>
            )}
            <Row label="Subject">
              <input aria-label="Subject" value={subject} onChange={(e) => setSubject(e.target.value)} className="min-w-0 flex-1 bg-transparent font-medium outline-none" />
            </Row>
          </div>
          <textarea
            ref={bodyRef}
            aria-label="Message"
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder={src && init.mode !== 'forward' ? 'Write your reply…' : 'Write your message…'}
            className="min-h-0 flex-1 resize-none bg-transparent px-4 py-3 text-sm leading-relaxed outline-none placeholder:text-ink-3"
          />
          {src && (
            <p className="shrink-0 px-4 pb-2 text-xs text-ink-3">
              {init.mode === 'forward' ? 'The original message is included below your text.' : 'The previous message is quoted below your reply.'}
            </p>
          )}
          {files.length > 0 && (
            <ul className="flex shrink-0 flex-wrap gap-2 px-4 pb-2">
              {files.map((f, i) => (
                <li key={i} className="flex items-center gap-1.5 rounded-lg bg-surface-2 py-1 pl-2.5 pr-1 text-xs">
                  <span className="max-w-40 truncate">{f.filename}</span>
                  <span className="text-ink-3">{formatBytes(f.size)}</span>
                  <button aria-label={`Remove ${f.filename}`} onClick={() => setFiles(files.filter((_, j) => j !== i))} className="rounded p-0.5 text-ink-3 hover:text-ink">
                    <X className="size-3.5" />
                  </button>
                </li>
              ))}
            </ul>
          )}
          <div className="shrink-0 px-4">
            <ErrorText>{error}</ErrorText>
          </div>
          <footer className="flex shrink-0 items-center gap-2 border-t border-line px-3 py-2.5 safe-bottom">
            <Button variant="primary" onClick={send} loading={sending}>
              Send
            </Button>
            <span className="hidden text-xs text-ink-3 sm:inline">⌘/Ctrl + Enter</span>
            <div className="flex-1" />
            <input ref={fileRef} type="file" multiple hidden onChange={(e) => addFiles(e.target.files).finally(() => (e.target.value = ''))} />
            <IconButton label="Attach files" onClick={() => fileRef.current?.click()}>
              <Paperclip className="size-4" />
            </IconButton>
            <Button variant="ghost" size="sm" onClick={discard}>
              Discard
            </Button>
          </footer>
        </>
      )}
    </div>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex h-11 items-center gap-2 px-4">
      <span className="w-14 shrink-0 text-[13px] text-ink-3">{label}</span>
      {children}
    </div>
  );
}

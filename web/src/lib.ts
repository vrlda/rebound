export function cx(...parts: (string | false | null | undefined)[]) {
  return parts.filter(Boolean).join(' ');
}

export function formatListTime(unix: number): string {
  const d = new Date(unix * 1000);
  const now = new Date();
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  const diffDays = (now.getTime() - d.getTime()) / 86400000;
  if (diffDays < 6) return d.toLocaleDateString([], { weekday: 'short' });
  if (d.getFullYear() === now.getFullYear()) return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  return d.toLocaleDateString([], { year: 'numeric', month: 'short', day: 'numeric' });
}

export function formatFullTime(unix: number): string {
  return new Date(unix * 1000).toLocaleString([], {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** Extracts the bare address from "Name <a@b.c>". */
export function bareAddress(s: string): string {
  const m = s.match(/<([^>]+)>/);
  return (m ? m[1] : s).trim().toLowerCase();
}

export function displayName(name: string, addr: string): string {
  return name?.trim() || addr;
}

export function initials(name: string, addr: string): string {
  const src = (name || addr.split('@')[0] || '?').replace(/[^\p{L}\p{N} ]/gu, ' ').trim();
  const parts = src.split(/\s+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '?') + (parts[1]?.[0] ?? '')).toUpperCase();
}

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
}

/** Plain text → minimal, safe HTML (escaped, links, line breaks). */
export function textToHtml(text: string): string {
  const linked = escapeHtml(text).replace(
    /\b(https?:\/\/[^\s<]+[^\s<.,;:!?)\]])/g,
    '<a href="$1">$1</a>',
  );
  return linked.replace(/\r?\n/g, '<br>');
}

export function splitRecipients(s: string): string[] {
  return s
    .split(/[,;\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

export function fileToBase64(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(',')[1] ?? '');
    r.onerror = () => reject(r.error);
    r.readAsDataURL(file);
  });
}

export function replySubject(subject: string, prefix: 'Re' | 'Fwd'): string {
  const re = prefix === 'Re' ? /^\s*re\s*:/i : /^\s*(fwd?|fw)\s*:/i;
  return re.test(subject) ? subject : `${prefix}: ${subject || '(no subject)'}`;
}

export function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = '='.repeat((4 - (base64.length % 4)) % 4);
  const raw = atob((base64 + padding).replace(/-/g, '+').replace(/_/g, '/'));
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

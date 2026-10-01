export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    credentials: 'same-origin',
    headers: {
      'X-Inbox': '1',
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!res.ok) {
    if (res.status === 401 && path !== '/auth/login') window.dispatchEvent(new Event('inbox:unauthorized'));
    throw new ApiError(res.status, data.error || `Request failed (${res.status})`);
  }
  return data as T;
}

export const api = {
  get: <T>(p: string) => request<T>('GET', p),
  post: <T>(p: string, b?: unknown) => request<T>('POST', p, b ?? {}),
  patch: <T>(p: string, b: unknown) => request<T>('PATCH', p, b),
  del: <T>(p: string) => request<T>('DELETE', p),
};

export type Folder = 'inbox' | 'starred' | 'sent' | 'archive' | 'spam' | 'trash';

export interface Account {
  id: number;
  name: string;
  color: string;
  identities: string[];
  default_from: string;
  signature: string;
  last_sync_at: number | null;
  last_error: string;
  key_hint: string;
}

export interface MessageRow {
  id: number;
  account_id: number;
  direction: 'in' | 'out';
  folder: string;
  thread_id: number;
  from_addr: string;
  from_name: string;
  to: string[] | null;
  subject: string;
  snippet: string;
  received_at: number;
  is_read: boolean;
  is_starred: boolean;
  spam_reason: string;
  attachments: number;
  thread_count: number;
}

export interface FileInfo {
  id: number;
  filename: string;
  content_type: string;
  size: number;
  content_id: string;
}

export interface MessageDetail extends MessageRow {
  message_id: string;
  references: string;
  cc: string[] | null;
  bcc: string[] | null;
  reply_to: string[] | null;
  text: string;
  html: string;
  files: FileInfo[];
}

export interface Counts {
  folders: Record<string, number>;
  accounts: Record<string, number>;
}

export interface Rule {
  id: number;
  pattern: string;
  action: 'spam' | 'inbox';
  created_at: number;
}

export interface SendAttachment {
  filename: string;
  content: string;
  content_type: string;
}

export interface SendPayload {
  account_id: number;
  from: string;
  to: string[];
  cc: string[];
  bcc: string[];
  subject: string;
  text: string;
  html: string;
  reply_to_id?: number;
  attachments: SendAttachment[];
}

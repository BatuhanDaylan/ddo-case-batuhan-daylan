// API istemcisi: aynı kaynak üzerinden /api (nginx → backend). 401'de refresh token ile bir kez yeniler.

const BASE = '/api';

export class ApiError extends Error {
  status: number;
  code: string;
  body: any;
  constructor(status: number, code: string, message: string, body: any) {
    super(message);
    this.status = status;
    this.code = code;
    this.body = body;
  }
  /** 422 kural ihlalleri / 409 çakışmalar gibi liste içeren hata ayrıntıları. */
  get items(): { code?: string; kind?: string; message: string }[] {
    const e = this.body?.error;
    return e?.violations ?? e?.conflicts ?? [];
  }
}

let access: string | null = localStorage.getItem('access');
let refresh: string | null = localStorage.getItem('refresh');
let onAuthLost: (() => void) | null = null;

export function setAuthLostHandler(fn: () => void) {
  onAuthLost = fn;
}

export function setTokens(a: string | null, r: string | null) {
  access = a;
  refresh = r;
  try {
    if (a && r) {
      localStorage.setItem('access', a);
      localStorage.setItem('refresh', r);
    } else {
      localStorage.removeItem('access');
      localStorage.removeItem('refresh');
    }
  } catch {
    /* depolama kapalıysa bellek içi çalışmaya devam eder */
  }
}

export const hasToken = () => !!access;

let refreshing: Promise<boolean> | null = null;

// Refresh token tek kullanımlıktır (rotasyon): eşzamanlı iki yenileme ikincisini "yeniden kullanım" saydırır.
// Bu yüzden aynı anda tek bir yenileme çalışır, diğer istekler onu bekler.
async function doRefresh(): Promise<boolean> {
  if (!refresh) return false;
  if (!refreshing) {
    refreshing = (async () => {
      const res = await fetch(`${BASE}/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: refresh }),
      });
      if (!res.ok) {
        setTokens(null, null);
        return false;
      }
      const j = await res.json();
      setTokens(j.access_token, j.refresh_token);
      return true;
    })().finally(() => {
      refreshing = null;
    });
  }
  return refreshing;
}

async function send(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<Response> {
  const h: Record<string, string> = { ...(headers ?? {}) };
  if (body !== undefined) h['Content-Type'] = 'application/json';
  if (access) h.Authorization = `Bearer ${access}`;
  return fetch(BASE + path, { method, headers: h, body: body !== undefined ? JSON.stringify(body) : undefined });
}

async function request(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<Response> {
  let res = await send(method, path, body, headers);
  if (res.status === 401 && refresh && !path.startsWith('/auth/')) {
    if (await doRefresh()) res = await send(method, path, body, headers);
    else onAuthLost?.();
  }
  return res;
}

export async function api<T = any>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  let res: Response;
  try {
    res = await request(method, path, body, headers);
  } catch {
    throw new ApiError(0, 'network', 'Sunucuya ulaşılamıyor. Bağlantınızı kontrol edin.', null);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: any = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    /* JSON olmayan yanıt */
  }
  if (!res.ok) throw new ApiError(res.status, data?.error?.code ?? 'error', data?.error?.message ?? res.statusText, data);
  return data as T;
}

export const get = <T = any>(path: string) => api<T>('GET', path);
export const post = <T = any>(path: string, body?: unknown, headers?: Record<string, string>) => api<T>('POST', path, body ?? {}, headers);
export const put = <T = any>(path: string, body?: unknown) => api<T>('PUT', path, body ?? {});
export const patch = <T = any>(path: string, body?: unknown) => api<T>('PATCH', path, body ?? {});
export const del = <T = any>(path: string) => api<T>('DELETE', path);

/** Kimlik doğrulamalı ikili indirme (PDF): tarayıcı Authorization başlığıyla doğrudan bağlantı açamaz. */
export async function downloadBlob(path: string): Promise<Blob> {
  const res = await request('GET', path);
  if (!res.ok) throw new ApiError(res.status, 'download', 'Dosya indirilemedi', null);
  return res.blob();
}

/** Keyset sayfalı bir listenin tüm sayfalarını (en fazla max öğe) getirir. */
export async function listAll<T = any>(path: string, max = 2000): Promise<T[]> {
  const out: T[] = [];
  let after = 0;
  const sep = path.includes('?') ? '&' : '?';
  for (;;) {
    const page = await get<{ items: T[]; next_after: number | null }>(`${path}${sep}limit=200&after=${after}`);
    out.push(...page.items);
    if (page.next_after == null || out.length >= max) break;
    after = page.next_after;
  }
  return out;
}

export const newKey = () => (crypto.randomUUID ? crypto.randomUUID() : String(Date.now()) + Math.random());

export function errText(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return e instanceof Error ? e.message : String(e);
}

import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { ApiError, errText } from './api';

/** Veri getirme kancası: yükleniyor / hata / veri durumlarını ve yeniden yüklemeyi yönetir. */
export function useFetch<T>(load: (() => Promise<T>) | null, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(load !== null);
  const [tick, setTick] = useState(0);
  const seq = useRef(0);

  useEffect(() => {
    if (!load) {
      setLoading(false);
      return;
    }
    const mine = ++seq.current;
    setLoading(true);
    setError(null);
    load()
      .then((d) => mine === seq.current && setData(d))
      .catch((e) => mine === seq.current && setError(errText(e)))
      .finally(() => mine === seq.current && setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading, reload, setData };
}

/** Yükleniyor (iskelet), hata (yeniden dene) ve boş durum için ortak sarmalayıcı. */
export function Async<T>(props: {
  state: { data: T | null; error: string | null; loading: boolean; reload: () => void };
  isEmpty?: (d: T) => boolean;
  empty?: string;
  children: (d: T) => ReactNode;
}) {
  const { state, isEmpty, empty = 'Gösterilecek kayıt yok.', children } = props;
  if (state.loading && state.data === null) {
    return (
      <div className="skeleton" aria-busy="true" aria-label="Yükleniyor">
        <div /> <div /> <div />
      </div>
    );
  }
  if (state.error) {
    return (
      <div className="alert error" role="alert">
        <span>{state.error}</span>
        <button className="btn small" onClick={state.reload}>Yeniden dene</button>
      </div>
    );
  }
  if (state.data === null) return null;
  if (isEmpty?.(state.data)) return <div className="empty">{empty}</div>;
  return <>{children(state.data)}</>;
}

export function Card(props: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`card ${props.className ?? ''}`}>
      {(props.title || props.actions) && (
        <header className="card-head">
          <h2>{props.title}</h2>
          <div className="row">{props.actions}</div>
        </header>
      )}
      {props.children}
    </section>
  );
}

export function PageTitle(props: { children: ReactNode; sub?: ReactNode }) {
  return (
    <div className="page-title">
      <h1>{props.children}</h1>
      {props.sub && <p className="muted">{props.sub}</p>}
    </div>
  );
}

export type Tone = 'ok' | 'warn' | 'bad' | 'info' | 'muted';
export const Badge = (props: { tone?: Tone; children: ReactNode }) => <span className={`badge ${props.tone ?? 'muted'}`}>{props.children}</span>;

export const Table = (props: { children: ReactNode }) => (
  <div className="table-wrap">
    <table>{props.children}</table>
  </div>
);

/** Hata/başarı mesajları: ApiError ise liste halindeki ayrıntıları (kural ihlalleri, çakışmalar) de gösterir. */
export function Notice(props: { error?: unknown; ok?: string | null; onClose?: () => void }) {
  if (props.ok) {
    return (
      <div className="alert ok" role="status">
        <span>{props.ok}</span>
        {props.onClose && <button className="btn small ghost" onClick={props.onClose}>Kapat</button>}
      </div>
    );
  }
  if (!props.error) return null;
  const items = props.error instanceof ApiError ? props.error.items : [];
  return (
    <div className="alert error" role="alert">
      <div>
        <strong>{errText(props.error)}</strong>
        {items.length > 0 && (
          <ul className="violations">
            {items.map((v, i) => (
              <li key={i}>
                {(v.code || v.kind) && <code>{v.code ?? v.kind}</code>} {v.message}
              </li>
            ))}
          </ul>
        )}
      </div>
      {props.onClose && <button className="btn small ghost" onClick={props.onClose}>Kapat</button>}
    </div>
  );
}

/** Eylem düğmesi: çalışırken devre dışı kalır ve hata/başarıyı bildirir. */
export function useAction() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [ok, setOk] = useState<string | null>(null);
  const run = useCallback(async (fn: () => Promise<string | void>) => {
    setBusy(true);
    setError(null);
    setOk(null);
    try {
      const msg = await fn();
      if (typeof msg === 'string') setOk(msg);
      return true;
    } catch (e) {
      setError(e);
      return false;
    } finally {
      setBusy(false);
    }
  }, []);
  const clear = useCallback(() => {
    setError(null);
    setOk(null);
  }, []);
  return { busy, error, ok, run, clear };
}

export function TermPicker(props: { terms: { id: number; code: string; name: string; registration_open: boolean }[]; value: number | null; onChange: (id: number) => void }) {
  return (
    <label className="field inline">
      <span>Dönem</span>
      <select value={props.value ?? ''} onChange={(e) => props.onChange(Number(e.target.value))}>
        {props.terms.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name} {t.registration_open ? '(kayıt açık)' : ''}
          </option>
        ))}
      </select>
    </label>
  );
}

export const fmtMinutes = (m: number) => `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
export const DAYS = ['Pzt', 'Sal', 'Çar', 'Per', 'Cum', 'Cmt', 'Paz'];

export const LETTER_TONE = (l: string): Tone => (l === 'FF' ? 'bad' : ['DD', 'DC'].includes(l) ? 'warn' : 'ok');

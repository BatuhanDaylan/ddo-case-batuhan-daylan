import { useRef, useState } from 'react';
import { ApiError, patch } from '../api';
import { Badge, DAYS, fmtMinutes } from '../ui';

export interface Meeting { id: number; course_code: string; day: number; start_min: number; end_min: number; room?: string }

const FIRST = 540; // 09:00
const SLOT = 60;
const SLOTS = 8;
const DAY_COUNT = 5;

interface Verdict { key: string; ok: boolean; conflicts: string[] }

/**
 * Haftalık program ızgarası: bir dersi başka hücreye sürükle-bırak (dokunmatikte: dersi seç, sonra hücreye dokun).
 * Üzerine gelinen hücre için sunucuya dry_run isteği atılır; çakışma bırakmadan ÖNCE kırmızıyla ve nedenleriyle görünür.
 * Asıl doğrulama her zaman sunucudadır (PATCH /section-meetings/{id}); arayüz yalnızca önizler.
 */
export function ScheduleEditor({ meetings, onChanged }: { meetings: Meeting[]; onChanged: () => void }) {
  const [picked, setPicked] = useState<number | null>(null);
  const [hover, setHover] = useState<string | null>(null);
  const [verdict, setVerdict] = useState<Verdict | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string; list?: string[] } | null>(null);
  const cache = useRef(new Map<string, Verdict>());
  const seq = useRef(0);

  const byId = new Map(meetings.map((m) => [m.id, m]));
  const cur = picked != null ? byId.get(picked) : undefined;

  const call = async (m: Meeting, day: number, slot: number, dryRun: boolean): Promise<Verdict> => {
    const key = `${m.id}:${day}:${slot}`;
    try {
      await patch(`/section-meetings/${m.id}`, { day, start_min: FIRST + slot * SLOT, dry_run: dryRun });
      return { key, ok: true, conflicts: [] };
    } catch (e) {
      if (e instanceof ApiError && (e.status === 409 || e.status === 400)) {
        const list: unknown = e.body?.error?.conflicts;
        return { key, ok: false, conflicts: Array.isArray(list) && list.length ? list.map(String) : [e.message] };
      }
      throw e;
    }
  };

  const preview = async (day: number, slot: number) => {
    if (!cur) return;
    const key = `${cur.id}:${day}:${slot}`;
    setHover(`${day}:${slot}`);
    const hit = cache.current.get(key);
    if (hit) return setVerdict(hit);
    const mine = ++seq.current;
    setVerdict(null);
    try {
      const v = await call(cur, day, slot, true);
      cache.current.set(key, v);
      if (mine === seq.current) setVerdict(v);
    } catch {
      /* ağ hatası: önizleme yok, bırakınca gerçek sonuç gösterilir */
    }
  };

  const drop = async (day: number, slot: number) => {
    if (!cur) return;
    if (cur.day === day && cur.start_min === FIRST + slot * SLOT) return setPicked(null);
    try {
      const v = await call(cur, day, slot, false);
      if (v.ok) {
        cache.current.clear();
        setMsg({ ok: true, text: `${cur.course_code} → ${DAYS[day - 1]} ${fmtMinutes(FIRST + slot * SLOT)} taşındı.` });
        setPicked(null);
        setVerdict(null);
        onChanged();
      } else {
        setMsg({ ok: false, text: `${cur.course_code} taşınamadı:`, list: v.conflicts });
      }
    } catch (e) {
      setMsg({ ok: false, text: e instanceof Error ? e.message : 'Taşıma başarısız.' });
    }
  };

  const cells = [];
  for (let s = 0; s < SLOTS; s++) {
    cells.push(<div key={`t${s}`} className="sched-time" style={{ gridColumn: 1, gridRow: s + 2 }}>{fmtMinutes(FIRST + s * SLOT)}</div>);
    for (let d = 1; d <= DAY_COUNT; d++) {
      const k = `${d}:${s}`;
      const state = cur && hover === k && verdict ? (verdict.ok ? 'good' : 'bad') : '';
      cells.push(
        <div
          key={k}
          className={`sched-cell ${state}`}
          style={{ gridColumn: d + 1, gridRow: s + 2 }}
          onDragOver={(e) => e.preventDefault()}
          onDragEnter={() => preview(d, s)}
          onDrop={(e) => { e.preventDefault(); drop(d, s); }}
          onClick={() => cur && drop(d, s)}
          onMouseEnter={() => cur && hover !== k && preview(d, s)}
        />,
      );
    }
  }

  return (
    <div>
      <p className="muted" style={{ marginTop: 0 }}>
        Bir dersi sürükleyip başka hücreye bırakın (dokunmatik ekranda: dersi seçin, sonra hedef hücreye dokunun). Hedef hücre bırakmadan önce{' '}
        <Badge tone="ok">yeşil</Badge> (uygun) ya da <Badge tone="bad">kırmızı</Badge> (çakışma) olur.
      </p>
      {msg && (
        <div className={`alert ${msg.ok ? 'ok' : 'error'}`} role="status">
          <div>
            <strong>{msg.text}</strong>
            {msg.list && <ul className="violations">{msg.list.map((c, i) => <li key={i}>{c}</li>)}</ul>}
          </div>
          <button className="btn small" onClick={() => setMsg(null)}>Kapat</button>
        </div>
      )}
      {cur && (
        <div className="muted" aria-live="polite" style={{ marginBottom: 8 }}>
          Seçili: <strong>{cur.course_code}</strong> ({DAYS[cur.day - 1]} {fmtMinutes(cur.start_min)}, {cur.room || 'derslik yok'})
          {verdict && hover && (verdict.ok ? ' · Bu hücreye taşınabilir.' : ` · Çakışma: ${verdict.conflicts.join('; ')}`)}
        </div>
      )}
      <div className="table-wrap">
        <div className="sched-grid" onMouseLeave={() => { setHover(null); setVerdict(null); }}>
          <div style={{ gridColumn: 1, gridRow: 1 }} />
          {DAYS.slice(0, DAY_COUNT).map((n, i) => <div key={n} className="sched-head" style={{ gridColumn: i + 2, gridRow: 1 }}>{n}</div>)}
          {cells}
          {meetings
            .filter((m) => m.day >= 1 && m.day <= DAY_COUNT && m.start_min >= FIRST)
            .map((m) => {
              const slot = Math.floor((m.start_min - FIRST) / SLOT);
              const span = Math.max(1, Math.round((m.end_min - m.start_min) / SLOT));
              if (slot >= SLOTS) return null;
              return (
                <button
                  key={m.id}
                  type="button"
                  draggable
                  className={`sched-block ${picked === m.id ? 'picked' : ''}`}
                  style={{ gridColumn: m.day + 1, gridRow: `${slot + 2} / span ${span}` }}
                  onDragStart={(e) => { e.dataTransfer.effectAllowed = 'move'; e.dataTransfer.setData('text/plain', String(m.id)); setPicked(m.id); setMsg(null); }}
                  onDragEnd={() => { setHover(null); setVerdict(null); }}
                  onClick={(e) => { e.stopPropagation(); setPicked(picked === m.id ? null : m.id); setMsg(null); setVerdict(null); }}
                  title={`${m.course_code} · ${m.room || '—'} · ${fmtMinutes(m.start_min)}–${fmtMinutes(m.end_min)}`}
                >
                  <strong>{m.course_code}</strong>
                  <span>{m.room || '—'}</span>
                </button>
              );
            })}
        </div>
      </div>
    </div>
  );
}

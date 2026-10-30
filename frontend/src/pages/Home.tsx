import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { ROLE_LABEL, useAuth, type Role } from '../auth';
import { Badge, Card, PageTitle } from '../ui';

const SHORTCUTS: Record<Role, { to: string; title: string; text: string }[]> = {
  student: [
    { to: '/enroll', title: 'Ders Kaydı', text: 'Şubelere kaydol, bekleme listesini ve kural ihlallerini gör.' },
    { to: '/schedule', title: 'Program', text: 'Haftalık ders programın ve sınav takvimin.' },
    { to: '/grades', title: 'Notlarım', text: 'Notlar, GANO, itiraz ve imzalı transkript.' },
    { to: '/graduation', title: 'Mezuniyet', text: 'Mezuniyet durumun ve kalan derslerin.' },
  ],
  instructor: [{ to: '/sections', title: 'Şubelerim', text: 'Not girişi, ilan, düzeltme talebi ve itirazlar.' }],
  advisor: [{ to: '/approvals', title: 'Onaylar', text: 'Danışanlarının kayıtlarını onayla/reddet; riskli öğrenci uyarılarını gör.' }],
  dept_secretary: [
    { to: '/academic', title: 'Akademik Yapı', text: 'Fakülte, bölüm, program ve dersler.' },
    { to: '/terms', title: 'Dönem & Şube', text: 'Kayıt dönemleri, şubeler, derslikler.' },
    { to: '/timetable', title: 'Program & Sınav', text: 'Ders programı çözücü ve sınav takvimi.' },
    { to: '/corrections', title: 'Düzeltmeler', text: 'Not düzeltme taleplerinde ikinci onay.' },
    { to: '/reports', title: 'Raporlar', text: 'Başarı, doluluk ve GANO dağılımı.' },
  ],
  registrar: [
    { to: '/academic', title: 'Akademik Yapı', text: 'Fakülte, bölüm, program ve dersler.' },
    { to: '/terms', title: 'Dönem & Şube', text: 'Kayıt dönemleri, şubeler, derslikler, tekrar-alma politikası.' },
    { to: '/timetable', title: 'Program & Sınav', text: 'Ders programı çözücü ve sınav takvimi.' },
    { to: '/corrections', title: 'Düzeltmeler', text: 'Not düzeltme taleplerinde ikinci onay.' },
    { to: '/reports', title: 'Raporlar', text: 'Başarı, doluluk ve GANO dağılımı.' },
  ],
  reporting: [{ to: '/reports', title: 'Raporlar', text: 'Bölüm bazlı başarı, doluluk ve GANO dağılımı.' }],
};

interface Live {
  ok: boolean;
  database: string;
  requests: number;
  inflight: number;
  outcomes: Record<string, number>;
  at: Date;
}

/** Prometheus metin biçimini ayrıştırır (yalnızca bu panelin ihtiyacı olan satırlar). */
function parseMetrics(text: string) {
  let requests = 0;
  let inflight = 0;
  const outcomes: Record<string, number> = {};
  for (const line of text.split('\n')) {
    if (line.startsWith('uninet_http_requests_total{')) requests += Number(line.split(' ').pop());
    else if (line.startsWith('uninet_http_requests_in_flight')) inflight = Number(line.split(' ').pop());
    else if (line.startsWith('uninet_enrollment_attempts_total{')) {
      const m = /outcome="([^"]+)"/.exec(line);
      if (m) outcomes[m[1]] = Number(line.split(' ').pop());
    }
  }
  return { requests, inflight, outcomes };
}

const HISTORY = 60;

/** Son örneklerin istek/sn değerlerini çizgi grafiği olarak gösterir (kayıt anındaki yoğunluğu görmek için). */
function RateChart({ points }: { points: number[] }) {
  const w = 600;
  const h = 80;
  const max = Math.max(1, ...points);
  const step = w / (HISTORY - 1);
  const x0 = (HISTORY - points.length) * step;
  const line = points.map((v, i) => `${(x0 + i * step).toFixed(1)},${(h - 4 - (v / max) * (h - 8)).toFixed(1)}`).join(' ');
  return (
    <svg viewBox={`0 0 ${w} ${h}`} width="100%" height={h} role="img" aria-label={`İstek/sn grafiği, en yüksek ${max.toFixed(1)}`} preserveAspectRatio="none">
      <line x1="0" y1={h - 1} x2={w} y2={h - 1} stroke="var(--line)" />
      {points.length > 1 && <polyline points={line} fill="none" stroke="var(--brand)" strokeWidth="2" vectorEffect="non-scaling-stroke" />}
    </svg>
  );
}

function LivePanel() {
  const [rates, setRates] = useState<number[]>([]);
  const last = useRef<{ n: number; t: number } | null>(null);
  const [live, setLive] = useState<Live | null>(null);
  const [err, setErr] = useState(false);

  useEffect(() => {
    let stop = false;
    const tick = async () => {
      try {
        const [h, m] = await Promise.all([fetch('/api/health', { cache: 'no-store' }), fetch('/api/metrics', { cache: 'no-store' })]);
        const hj = await h.json().catch(() => ({}));
        const mt = parseMetrics(await m.text());
        if (!stop) {
          const now = Date.now();
          if (last.current) {
            // Panelin kendi iki isteği (health + metrics) de sayıldığı için düşülür.
            const rate = Math.max(0, (mt.requests - last.current.n - 2) / ((now - last.current.t) / 1000));
            setRates((prev) => [...prev, rate].slice(-HISTORY));
          }
          last.current = { n: mt.requests, t: now };
          setLive({ ok: h.ok, database: hj.database ?? '?', ...mt, at: new Date() });
          setErr(false);
        }
      } catch {
        if (!stop) setErr(true);
      }
    };
    tick();
    const id = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, []);

  return (
    <Card title="Sistem durumu (canlı)" actions={live && <span className="muted" style={{ margin: 0, fontSize: '0.8rem' }}>Son güncelleme {live.at.toLocaleTimeString('tr-TR')}</span>}>
      {err && <div className="alert error" role="alert">Sunucuya ulaşılamıyor. Otomatik olarak yeniden denenecek.</div>}
      {!live && !err && <div className="skeleton"><div /><div /></div>}
      {live && (
        <div className="grid cols-4">
          <div className="stat">
            <b>{live.ok ? <Badge tone="ok">Çalışıyor</Badge> : <Badge tone="bad">Sorun var</Badge>}</b>
            <span>API · veritabanı: {live.database}</span>
          </div>
          <div className="stat"><b>{live.requests.toLocaleString('tr-TR')}</b><span>toplam HTTP isteği</span></div>
          <div className="stat"><b>{live.inflight}</b><span>şu an işlenen istek</span></div>
          <div className="stat">
            <b>{((live.outcomes.enrolled ?? 0) + (live.outcomes.waitlisted ?? 0)).toLocaleString('tr-TR')}</b>
            <span>
              kayıt denemesi · {(live.outcomes.enrolled ?? 0).toLocaleString('tr-TR')} kayıtlı, {(live.outcomes.waitlisted ?? 0).toLocaleString('tr-TR')} bekleme
              {live.outcomes.rule_violation ? `, ${live.outcomes.rule_violation} kural ihlali` : ''}
            </span>
          </div>
        </div>
      )}
      {live && (
        <div style={{ marginTop: '1rem' }}>
          <div className="muted" style={{ fontSize: '0.8rem', marginBottom: 4 }}>
            İstek/sn (son {HISTORY * 2} sn){rates.length > 0 && ` · şu an ${rates[rates.length - 1].toFixed(1)}, en yüksek ${Math.max(...rates).toFixed(1)}`}
          </div>
          <RateChart points={rates} />
        </div>
      )}
    </Card>
  );
}

export default function Home() {
  const { user } = useAuth();
  if (!user) return null;
  return (
    <div className="stack">
      <PageTitle sub={`${ROLE_LABEL[user.role]} olarak giriş yaptınız.`}>Hoş geldiniz, {user.full_name}</PageTitle>
      <div className={`grid ${SHORTCUTS[user.role].length === 4 ? 'cols-4' : 'cols-3'}`}>
        {SHORTCUTS[user.role].map((s) => (
          <Link key={s.to} to={s.to} style={{ textDecoration: 'none', color: 'inherit' }}>
            <Card className="shortcut">
              <h2 style={{ color: 'var(--brand)' }}>{s.title} →</h2>
              <p className="muted">{s.text}</p>
            </Card>
          </Link>
        ))}
      </div>
      <LivePanel />
    </div>
  );
}

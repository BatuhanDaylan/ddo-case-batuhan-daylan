import { useEffect, useState } from 'react';
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
  advisor: [{ to: '/approvals', title: 'Onaylar', text: 'Danışanlarının ders kayıtlarını onayla ya da reddet.' }],
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

function LivePanel() {
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
          setLive({ ok: h.ok, database: hj.database ?? '?', ...mt, at: new Date() });
          setErr(false);
        }
      } catch {
        if (!stop) setErr(true);
      }
    };
    tick();
    const id = setInterval(tick, 5000);
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

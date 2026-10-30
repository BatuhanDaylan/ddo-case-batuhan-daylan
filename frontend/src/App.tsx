import { useEffect, useState, type ReactNode } from 'react';
import { Link, NavLink, Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { ROLE_LABEL, useAuth, type Role } from './auth';
import Home from './pages/Home';
import Login from './pages/Login';
import Verify from './pages/Verify';
import { Enroll, Grades, Graduation, Schedule } from './pages/student';
import { MySections, SectionDetail } from './pages/instructor';
import { Approvals } from './pages/advisor';
import { Academic, Corrections, TermsAndSections, Timetable } from './pages/admin';
import Reports from './pages/Reports';

const STAFF: Role[] = ['dept_secretary', 'registrar'];

const NAV: Record<Role, { to: string; label: string }[]> = {
  student: [
    { to: '/enroll', label: 'Ders Kaydı' },
    { to: '/schedule', label: 'Program' },
    { to: '/grades', label: 'Notlarım' },
    { to: '/graduation', label: 'Mezuniyet' },
  ],
  instructor: [{ to: '/sections', label: 'Şubelerim' }],
  advisor: [{ to: '/approvals', label: 'Onaylar' }],
  dept_secretary: [
    { to: '/academic', label: 'Akademik Yapı' },
    { to: '/terms', label: 'Dönem & Şube' },
    { to: '/timetable', label: 'Program & Sınav' },
    { to: '/corrections', label: 'Düzeltmeler' },
    { to: '/reports', label: 'Raporlar' },
  ],
  registrar: [
    { to: '/academic', label: 'Akademik Yapı' },
    { to: '/terms', label: 'Dönem & Şube' },
    { to: '/timetable', label: 'Program & Sınav' },
    { to: '/corrections', label: 'Düzeltmeler' },
    { to: '/reports', label: 'Raporlar' },
  ],
  reporting: [{ to: '/reports', label: 'Raporlar' }],
};

/** Sağlık ucunu düzenli yoklar; sistemin canlı olduğunu başlıkta gösterir. */
function useLive() {
  const [state, setState] = useState<'unknown' | 'live' | 'down'>('unknown');
  useEffect(() => {
    let stop = false;
    const ping = async () => {
      try {
        const r = await fetch('/api/health', { cache: 'no-store' });
        if (!stop) setState(r.ok ? 'live' : 'down');
      } catch {
        if (!stop) setState('down');
      }
    };
    ping();
    const id = setInterval(ping, 10000);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, []);
  return state;
}

function Guard({ roles, children }: { roles?: Role[]; children: ReactNode }) {
  const { user, loading } = useAuth();
  const loc = useLocation();
  if (loading) return <div className="center"><div className="skeleton" style={{ width: 280 }}><div /><div /></div></div>;
  if (!user) return <Navigate to="/login" state={{ from: loc.pathname }} replace />;
  if (roles && !roles.includes(user.role)) return <Navigate to="/" replace />;
  return <>{children}</>;
}

function Layout({ children }: { children: ReactNode }) {
  const { user, logout } = useAuth();
  const live = useLive();
  const [open, setOpen] = useState(false);
  const loc = useLocation();
  useEffect(() => setOpen(false), [loc.pathname]);
  if (!user) return null;
  return (
    <>
      <header className="topbar">
        <div className="topbar-inner">
          <Link to="/" className="brand">UniNet</Link>
          <nav className={`nav ${open ? 'open' : ''}`} aria-label="Ana menü">
            <NavLink to="/" end>Ana sayfa</NavLink>
            {NAV[user.role].map((n) => (
              <NavLink key={n.to} to={n.to}>{n.label}</NavLink>
            ))}
          </nav>
          <span className="spacer" />
          <span className="status-pill" title={live === 'live' ? 'API ve veritabanı yanıt veriyor' : 'Sunucuya ulaşılamıyor'}>
            <i className={`dot ${live === 'live' ? 'live' : live === 'down' ? 'down' : ''}`} />
            <span className="label">{live === 'live' ? 'Sistem canlı' : live === 'down' ? 'Bağlantı yok' : 'Denetleniyor…'}</span>
          </span>
          <span className="muted" style={{ margin: 0, fontSize: '0.85rem' }} title={user.email}>{ROLE_LABEL[user.role]}</span>
          <button className="btn small" onClick={logout}>Çıkış</button>
          <button className="btn small menu-btn" aria-expanded={open} aria-label="Menüyü aç/kapat" onClick={() => setOpen((o) => !o)}>Menü</button>
        </div>
      </header>
      <main>{children}</main>
    </>
  );
}

const page = (el: ReactNode, roles?: Role[]) => (
  <Guard roles={roles}>
    <Layout>{el}</Layout>
  </Guard>
);

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/verify/:code" element={<Verify />} />
      <Route path="/" element={page(<Home />)} />
      <Route path="/enroll" element={page(<Enroll />, ['student'])} />
      <Route path="/schedule" element={page(<Schedule />, ['student'])} />
      <Route path="/grades" element={page(<Grades />, ['student'])} />
      <Route path="/graduation" element={page(<Graduation />, ['student'])} />
      <Route path="/sections" element={page(<MySections />, ['instructor'])} />
      <Route path="/sections/:id" element={page(<SectionDetail />, ['instructor'])} />
      <Route path="/approvals" element={page(<Approvals />, ['advisor'])} />
      <Route path="/academic" element={page(<Academic />, STAFF)} />
      <Route path="/terms" element={page(<TermsAndSections />, STAFF)} />
      <Route path="/timetable" element={page(<Timetable />, STAFF)} />
      <Route path="/corrections" element={page(<Corrections />, STAFF)} />
      <Route path="/reports" element={page(<Reports />, ['reporting', 'registrar', 'dept_secretary'])} />
      <Route path="*" element={page(<div className="empty">Sayfa bulunamadı. <Link to="/">Ana sayfaya dön</Link></div>)} />
    </Routes>
  );
}

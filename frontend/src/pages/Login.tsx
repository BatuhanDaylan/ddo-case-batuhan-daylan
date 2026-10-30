import { useState, type FormEvent } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { errText } from '../api';
import { ROLE_LABEL, useAuth, type Role } from '../auth';

const DEMO: { role: Role; email: string }[] = [
  { role: 'student', email: 'ogrenci@uninet.test' },
  { role: 'instructor', email: 'hoca@uninet.test' },
  { role: 'advisor', email: 'danisman@uninet.test' },
  { role: 'dept_secretary', email: 'sekreter@uninet.test' },
  { role: 'registrar', email: 'ogrenciisleri@uninet.test' },
  { role: 'reporting', email: 'rektorluk@uninet.test' },
];

export default function Login() {
  const { user, login } = useAuth();
  const nav = useNavigate();
  const loc = useLocation();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (user) return <Navigate to={(loc.state as { from?: string } | null)?.from ?? '/'} replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login(email, password);
      nav((loc.state as { from?: string } | null)?.from ?? '/', { replace: true });
    } catch (err) {
      setError(errText(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="center">
      <div className="card login stack">
        <div>
          <div className="brand" style={{ fontSize: '1.5rem' }}>UniNet</div>
          <p className="muted">Üniversite Öğrenci Bilgi Sistemi</p>
        </div>
        <form className="form" onSubmit={submit}>
          <label className="field">
            <span>E-posta</span>
            <input type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </label>
          <label className="field">
            <span>Parola</span>
            <input type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
          </label>
          {error && <div className="alert error" role="alert">{error}</div>}
          <button className="btn primary" disabled={busy}>{busy ? 'Giriş yapılıyor…' : 'Giriş yap'}</button>
        </form>
        <div>
          <p className="muted" style={{ marginBottom: 8 }}>Demo hesaplar (parola: <code>Demo1234!</code>) — tıklayınca alanlar dolar:</p>
          <div className="demo-accounts">
            {DEMO.map((d) => (
              <button type="button" key={d.email} className="btn small" onClick={() => { setEmail(d.email); setPassword('Demo1234!'); }}>
                {ROLE_LABEL[d.role]}
              </button>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}

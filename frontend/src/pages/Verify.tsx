import { Link, useParams } from 'react-router-dom';
import { ApiError, get } from '../api';
import { Async, Badge, Card, useFetch } from '../ui';

interface Doc {
  valid: boolean;
  document_id: number;
  kind: string;
  issued_at: string;
  full_name: string;
  student_no: string;
  program: string;
  gano: number;
  course_count: number;
}

/** Herkese açık belge doğrulama sayfası: QR kod bu adrese yönlendirir (kimlik gerekmez). */
export default function Verify() {
  const { code = '' } = useParams();
  const s = useFetch<Doc | 'invalid'>(
    () =>
      get<Doc>(`/verify/${encodeURIComponent(code)}`).catch((e) => {
        if (e instanceof ApiError && e.status === 404) return 'invalid' as const;
        throw e;
      }),
    [code],
  );
  return (
    <div className="center">
      <div className="card login stack">
        <div>
          <Link to="/" className="brand">UniNet</Link>
          <p className="muted">Belge doğrulama</p>
        </div>
        <Async state={s}>
          {(d) =>
            d === 'invalid' ? (
              <div className="alert error" role="alert">
                <div>
                  <strong>Belge doğrulanamadı.</strong>
                  <p className="muted" style={{ color: 'inherit' }}>Kod geçersiz, bozulmuş ya da bu sistem tarafından düzenlenmemiş olabilir. Belge sahte olabilir.</p>
                </div>
              </div>
            ) : (
              <Card title={<>Geçerli belge <Badge tone="ok">İmza doğrulandı</Badge></>}>
                <p className="muted" style={{ marginTop: 0 }}>Aşağıdaki bilgileri elinizdeki belgeyle karşılaştırın.</p>
                <table>
                  <tbody>
                    <tr><th>Ad soyad</th><td>{d.full_name}</td></tr>
                    <tr><th>Öğrenci no</th><td>{d.student_no}</td></tr>
                    <tr><th>Program</th><td>{d.program}</td></tr>
                    <tr><th>Ders sayısı</th><td>{d.course_count}</td></tr>
                    <tr><th>GANO</th><td>{d.gano.toFixed(2)}</td></tr>
                    <tr><th>Düzenlenme</th><td>{new Date(d.issued_at).toLocaleString('tr-TR')}</td></tr>
                    <tr><th>Belge no</th><td>{d.document_id}</td></tr>
                  </tbody>
                </table>
              </Card>
            )
          }
        </Async>
      </div>
    </div>
  );
}

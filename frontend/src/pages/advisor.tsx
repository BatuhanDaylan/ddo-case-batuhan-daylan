import { get, listAll, post } from '../api';
import { Async, Badge, Card, Notice, PageTitle, Table, useAction, useFetch } from '../ui';

interface Row {
  enrollment_id: number;
  student_no: string;
  course_code: string;
  status: 'enrolled' | 'waitlisted';
  advisor_status: 'pending' | 'approved' | 'rejected';
}

interface Risk {
  student_id: number;
  student_no: string;
  gano: number;
  last_term_gpa: number;
  prev_term_gpa: number | null;
  failed_in_last_term: number;
  reasons: ('low_gano' | 'gpa_drop' | 'failed_courses')[];
}

const REASON: Record<Risk['reasons'][number], string> = {
  low_gano: 'GANO 2,00 altında',
  gpa_drop: 'Dönem ortalaması düştü',
  failed_courses: 'Son dönemde 2+ dersten kaldı',
};

function AtRisk() {
  const risk = useFetch(() => get<{ items: Risk[] }>('/advisor/at-risk').then((r) => r.items), []);
  return (
    <Card title="Riskli öğrenciler">
      <Async state={risk} isEmpty={(d) => d.length === 0} empty="Riskli öğrenci yok.">
        {(d) => (
          <Table>
            <thead><tr><th>Öğrenci no</th><th>GANO</th><th>Son dönem</th><th>Uyarılar</th></tr></thead>
            <tbody>
              {d.map((r) => (
                <tr key={r.student_id}>
                  <td>{r.student_no}</td>
                  <td>{r.gano.toFixed(2)}</td>
                  <td>{r.last_term_gpa.toFixed(2)}{r.prev_term_gpa != null && ` (önceki ${r.prev_term_gpa.toFixed(2)})`}</td>
                  <td><div className="row">{r.reasons.map((x) => <Badge key={x} tone="warn">{REASON[x]}</Badge>)}</div></td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Async>
    </Card>
  );
}

export function Approvals() {
  const list = useFetch(() => listAll<Row>('/advisor/enrollments?status=pending'), []);
  const act = useAction();

  const decide = (r: Row, approve: boolean) =>
    act.run(async () => {
      const res = await post<{ promoted_enrollment_id?: number }>(`/enrollments/${r.enrollment_id}/advisor-decision`, { approve });
      list.reload();
      if (approve) return `${r.student_no} · ${r.course_code} kaydı onaylandı.`;
      return `${r.student_no} · ${r.course_code} kaydı reddedildi.${res.promoted_enrollment_id ? ' Boşalan koltuk bekleme listesinin ilk öğrencisine atandı.' : ''}`;
    });

  return (
    <div className="stack">
      <PageTitle sub="Yalnızca danışmanı olduğunuz öğrencilerin kayıtları listelenir.">Kayıt Onayları</PageTitle>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
      <Card title="Onay bekleyen kayıtlar">
        <Async state={list} isEmpty={(d) => d.length === 0} empty="Onay bekleyen kayıt yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Öğrenci no</th><th>Ders</th><th>Durum</th><th /></tr></thead>
              <tbody>
                {d.map((r) => (
                  <tr key={r.enrollment_id}>
                    <td>{r.student_no}</td>
                    <td><strong>{r.course_code}</strong></td>
                    <td>{r.status === 'enrolled' ? <Badge tone="ok">Kayıtlı</Badge> : <Badge tone="warn">Bekleme listesinde</Badge>}</td>
                    <td>
                      <div className="row">
                        <button className="btn small primary" disabled={act.busy} onClick={() => decide(r, true)}>Onayla</button>
                        <button className="btn small danger" disabled={act.busy} onClick={() => decide(r, false)}>Reddet</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Async>
      </Card>
      <AtRisk />
    </div>
  );
}

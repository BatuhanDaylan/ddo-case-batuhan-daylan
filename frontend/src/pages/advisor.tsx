import { listAll, post } from '../api';
import { Async, Badge, Card, Notice, PageTitle, Table, useAction, useFetch } from '../ui';

interface Row {
  enrollment_id: number;
  student_no: string;
  course_code: string;
  status: 'enrolled' | 'waitlisted';
  advisor_status: 'pending' | 'approved' | 'rejected';
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
    </div>
  );
}

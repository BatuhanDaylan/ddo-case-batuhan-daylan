import { get } from '../api';
import { Async, Card, PageTitle, Table, TermPicker, useFetch } from '../ui';
import { useTermSelect } from './shared';

interface Success { department_id: number; department: string; results: number; passed: number; pass_rate: number; avg_grade_point: number }
interface Occupancy { department_id: number; department: string; sections: number; capacity: number; enrolled: number; waitlisted: number; fill_rate: number }
interface Dist { total_students: number; buckets: { range: string; count: number }[] }

function Bar({ value, max = 100, tone = '' }: { value: number; max?: number; tone?: string }) {
  return <div className={`bar ${tone}`} role="img" aria-label={`${value}`}><i style={{ width: `${Math.min(100, max ? (value / max) * 100 : 0)}%` }} /></div>;
}

export default function Reports() {
  const { terms, state: termState, termId, setTermId } = useTermSelect();
  const success = useFetch(termId ? () => get<{ items: Success[] }>(`/reports/department-success?term_id=${termId}`) : null, [termId]);
  const occ = useFetch(termId ? () => get<{ items: Occupancy[] }>(`/reports/occupancy?term_id=${termId}`) : null, [termId]);
  const dist = useFetch(() => get<Dist>('/reports/gano-distribution'), []);

  return (
    <div className="stack">
      <PageTitle sub="Toplu, kişisel veri içermeyen raporlar.">Raporlar</PageTitle>
      <Card title="Dönem" actions={<Async state={termState}>{() => <TermPicker terms={terms} value={termId} onChange={setTermId} />}</Async>}>
        <p className="muted" style={{ margin: 0 }}>Başarı ve doluluk raporları seçili dönem içindir; GANO dağılımı tüm öğrencileri kapsar.</p>
      </Card>

      <Card title="Bölüm bazlı başarı">
        <Async state={success} isEmpty={(d) => d.items.length === 0} empty="Bu dönem için not verisi yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Bölüm</th><th className="num">Sonuç</th><th className="num">Geçen</th><th style={{ minWidth: 160 }}>Başarı oranı</th><th className="num">Ort. katsayı</th></tr></thead>
              <tbody>
                {d.items.map((x) => (
                  <tr key={x.department_id}>
                    <td><strong>{x.department}</strong></td>
                    <td className="num">{x.results.toLocaleString('tr-TR')}</td>
                    <td className="num">{x.passed.toLocaleString('tr-TR')}</td>
                    <td><Bar value={x.pass_rate} tone={x.pass_rate >= 75 ? 'ok' : ''} /><small className="muted">%{x.pass_rate.toFixed(1)}</small></td>
                    <td className="num">{x.avg_grade_point.toFixed(2)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Async>
      </Card>

      <Card title="Şube doluluğu">
        <Async state={occ} isEmpty={(d) => d.items.length === 0} empty="Bu dönem için şube yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Bölüm</th><th className="num">Şube</th><th className="num">Kontenjan</th><th className="num">Kayıtlı</th><th className="num">Bekleyen</th><th style={{ minWidth: 160 }}>Doluluk</th></tr></thead>
              <tbody>
                {d.items.map((x) => (
                  <tr key={x.department_id}>
                    <td><strong>{x.department}</strong></td>
                    <td className="num">{x.sections}</td>
                    <td className="num">{x.capacity.toLocaleString('tr-TR')}</td>
                    <td className="num">{x.enrolled.toLocaleString('tr-TR')}</td>
                    <td className="num">{x.waitlisted.toLocaleString('tr-TR')}</td>
                    <td><Bar value={x.fill_rate} tone={x.fill_rate >= 95 ? 'ok' : ''} /><small className="muted">%{x.fill_rate.toFixed(1)}</small></td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Async>
      </Card>

      <Card title="GANO dağılımı">
        <Async state={dist} isEmpty={(d) => d.total_students === 0} empty="Henüz hesaplanmış GANO yok.">
          {(d) => {
            const max = Math.max(...d.buckets.map((b) => b.count), 1);
            return (
              <>
                <p className="muted" style={{ marginTop: 0 }}>{d.total_students.toLocaleString('tr-TR')} öğrenci</p>
                <div style={{ display: 'grid', gap: 8 }}>
                  {d.buckets.map((b) => (
                    <div key={b.range} className="row" style={{ flexWrap: 'nowrap' }}>
                      <span style={{ width: 86, fontVariantNumeric: 'tabular-nums' }}>{b.range}</span>
                      <div style={{ flex: 1 }}><Bar value={b.count} max={max} /></div>
                      <span style={{ width: 64, textAlign: 'right', fontVariantNumeric: 'tabular-nums' }}>{b.count.toLocaleString('tr-TR')}</span>
                    </div>
                  ))}
                </div>
              </>
            );
          }}
        </Async>
      </Card>
    </div>
  );
}

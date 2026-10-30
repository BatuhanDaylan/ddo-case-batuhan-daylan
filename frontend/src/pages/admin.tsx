import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { listAll, patch, post, put } from '../api';
import { useAuth } from '../auth';
import { Async, Badge, Card, DAYS, Notice, PageTitle, Table, TermPicker, fmtMinutes, useAction, useFetch } from '../ui';
import { ScheduleEditor } from './ScheduleEditor';
import { useTermSelect, type Sec, type Term } from './shared';

interface Faculty { id: number; code: string; name: string }
interface Dept { id: number; faculty_id: number; code: string; name: string }
interface Program { id: number; department_id: number; code: string; name: string; total_ects_required: number }
interface Course { id: number; code: string; name: string; ects: number; department_id: number | null }
interface Room { id: number; code: string; capacity: number; kind: string }

const KIND_LABEL: Record<string, string> = { lecture: 'Sınıf', lab: 'Laboratuvar', amphi: 'Amfi' };

function Field(props: { label: string; children: ReactNode }) {
  return <label className="field"><span>{props.label}</span>{props.children}</label>;
}

/** Küçük bir "oluştur" formu: gönderirken devre dışı kalır, hata/başarıyı gösterir. */
function CreateForm(props: { title: string; onSubmit: () => Promise<string | void>; children: ReactNode; submitLabel?: string }) {
  const act = useAction();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    return act.run(props.onSubmit);
  };
  return (
    <form className="form" onSubmit={submit} style={{ marginTop: 16 }}>
      <strong>{props.title}</strong>
      <div className="form cols">{props.children}<button className="btn primary" disabled={act.busy}>{props.submitLabel ?? 'Oluştur'}</button></div>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
    </form>
  );
}

/* --------------------------------------------------------------- Akademik yapı --------------------------------------------------------------- */

export function Academic() {
  const [tab, setTab] = useState<'faculties' | 'departments' | 'programs' | 'courses'>('faculties');
  const faculties = useFetch(() => listAll<Faculty>('/faculties'), []);
  const depts = useFetch(() => listAll<Dept>('/departments'), []);
  const programs = useFetch(() => listAll<Program>('/programs'), []);
  const courses = useFetch(() => listAll<Course>('/courses', 1000), []);
  const [f, setF] = useState({ code: '', name: '', faculty_id: '', department_id: '', ects: '240', courseEcts: '5' });

  const facName = (id: number) => faculties.data?.find((x) => x.id === id)?.name ?? id;
  const deptName = (id: number | null) => (id == null ? '—' : depts.data?.find((x) => x.id === id)?.name ?? id);

  return (
    <div className="stack">
      <PageTitle sub="Fakülte → bölüm → program → ders hiyerarşisi. Yazma yetkisi öğrenci işleri ve bölüm sekreterindedir.">Akademik Yapı</PageTitle>
      <div className="tabs" role="tablist">
        {([['faculties', 'Fakülteler'], ['departments', 'Bölümler'], ['programs', 'Programlar'], ['courses', 'Dersler']] as const).map(([k, l]) => (
          <button key={k} role="tab" aria-selected={tab === k} className={tab === k ? 'active' : ''} onClick={() => setTab(k)}>{l}</button>
        ))}
      </div>

      {tab === 'faculties' && (
        <Card title="Fakülteler">
          <Async state={faculties} isEmpty={(d) => d.length === 0} empty="Fakülte yok.">
            {(d) => <Table><thead><tr><th>Kod</th><th>Ad</th></tr></thead><tbody>{d.map((x) => <tr key={x.id}><td><code>{x.code}</code></td><td>{x.name}</td></tr>)}</tbody></Table>}
          </Async>
          <CreateForm title="Yeni fakülte" onSubmit={async () => { await post('/faculties', { code: f.code, name: f.name }); faculties.reload(); setF({ ...f, code: '', name: '' }); return 'Fakülte oluşturuldu.'; }}>
            <Field label="Kod"><input required value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} /></Field>
            <Field label="Ad"><input required value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
          </CreateForm>
        </Card>
      )}

      {tab === 'departments' && (
        <Card title="Bölümler">
          <Async state={depts} isEmpty={(d) => d.length === 0} empty="Bölüm yok.">
            {(d) => <Table><thead><tr><th>Kod</th><th>Ad</th><th>Fakülte</th></tr></thead><tbody>{d.map((x) => <tr key={x.id}><td><code>{x.code}</code></td><td>{x.name}</td><td>{facName(x.faculty_id)}</td></tr>)}</tbody></Table>}
          </Async>
          <CreateForm title="Yeni bölüm" onSubmit={async () => { await post('/departments', { faculty_id: Number(f.faculty_id), code: f.code, name: f.name }); depts.reload(); setF({ ...f, code: '', name: '' }); return 'Bölüm oluşturuldu.'; }}>
            <Field label="Fakülte"><select required value={f.faculty_id} onChange={(e) => setF({ ...f, faculty_id: e.target.value })}><option value="">Seçin…</option>{(faculties.data ?? []).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></Field>
            <Field label="Kod"><input required value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} /></Field>
            <Field label="Ad"><input required value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
          </CreateForm>
        </Card>
      )}

      {tab === 'programs' && (
        <Card title="Programlar">
          <Async state={programs} isEmpty={(d) => d.length === 0} empty="Program yok.">
            {(d) => <Table><thead><tr><th>Kod</th><th>Ad</th><th>Bölüm</th><th className="num">Gereken AKTS</th></tr></thead><tbody>{d.map((x) => <tr key={x.id}><td><code>{x.code}</code></td><td>{x.name}</td><td>{deptName(x.department_id)}</td><td className="num">{x.total_ects_required}</td></tr>)}</tbody></Table>}
          </Async>
          <CreateForm title="Yeni program" onSubmit={async () => { await post('/programs', { department_id: Number(f.department_id), code: f.code, name: f.name, total_ects_required: Number(f.ects) }); programs.reload(); setF({ ...f, code: '', name: '' }); return 'Program oluşturuldu.'; }}>
            <Field label="Bölüm"><select required value={f.department_id} onChange={(e) => setF({ ...f, department_id: e.target.value })}><option value="">Seçin…</option>{(depts.data ?? []).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></Field>
            <Field label="Kod"><input required value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} /></Field>
            <Field label="Ad"><input required value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
            <Field label="Gereken AKTS"><input type="number" required min={1} value={f.ects} onChange={(e) => setF({ ...f, ects: e.target.value })} /></Field>
          </CreateForm>
        </Card>
      )}

      {tab === 'courses' && (
        <Card title="Dersler">
          <Async state={courses} isEmpty={(d) => d.length === 0} empty="Ders yok.">
            {(d) => <Table><thead><tr><th>Kod</th><th>Ad</th><th className="num">AKTS</th><th>Bölüm</th></tr></thead><tbody>{d.slice(0, 200).map((x) => <tr key={x.id}><td><code>{x.code}</code></td><td>{x.name}</td><td className="num">{x.ects}</td><td>{deptName(x.department_id)}</td></tr>)}</tbody></Table>}
          </Async>
          {(courses.data?.length ?? 0) > 200 && <p className="muted">İlk 200 ders gösteriliyor (toplam {courses.data?.length}).</p>}
          <CreateForm title="Yeni ders" onSubmit={async () => { await post('/courses', { code: f.code, name: f.name, ects: Number(f.courseEcts), department_id: f.department_id ? Number(f.department_id) : undefined }); courses.reload(); setF({ ...f, code: '', name: '' }); return 'Ders oluşturuldu.'; }}>
            <Field label="Kod"><input required value={f.code} onChange={(e) => setF({ ...f, code: e.target.value })} /></Field>
            <Field label="Ad"><input required value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} /></Field>
            <Field label="AKTS"><input type="number" required min={1} max={60} value={f.courseEcts} onChange={(e) => setF({ ...f, courseEcts: e.target.value })} /></Field>
            <Field label="Bölüm"><select value={f.department_id} onChange={(e) => setF({ ...f, department_id: e.target.value })}><option value="">(yok)</option>{(depts.data ?? []).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}</select></Field>
          </CreateForm>
        </Card>
      )}
    </div>
  );
}

/* --------------------------------------------------------------- Dönem, şube, derslik --------------------------------------------------------------- */

export function TermsAndSections() {
  const { user } = useAuth();
  const terms = useFetch(() => listAll<Term & { drop_deadline?: string }>('/terms'), []);
  const { terms: termList, termId, setTermId, state: termState } = useTermSelect();
  const sections = useFetch(termId ? () => listAll<Sec>(`/sections?term_id=${termId}`, 2000) : null, [termId]);
  const courses = useFetch(() => listAll<Course>('/courses', 1000), []);
  const rooms = useFetch(() => listAll<Room>('/rooms'), []);
  const programs = useFetch(() => listAll<Program>('/programs'), []);
  const act = useAction();
  const [t, setT] = useState({ code: '', name: '' });
  const [s, setS] = useState({ course: '', code: '01', capacity: '40', instructor: '' });
  const [r, setR] = useState({ code: '', capacity: '40', kind: 'lecture' });
  const [deadline, setDeadline] = useState('');
  const [pol, setPol] = useState({ program: '', policy: 'last' });
  const [shown, setShown] = useState(25);

  const toggle = (x: Term) =>
    act.run(async () => {
      await patch(`/terms/${x.id}`, { registration_open: !x.registration_open });
      terms.reload();
      termState.reload();
      return `${x.name} için kayıt ${x.registration_open ? 'kapatıldı' : 'açıldı'}.`;
    });

  return (
    <div className="stack">
      <PageTitle sub="Her değişiklik denetim kaydına yazılır.">Dönem & Şube Yönetimi</PageTitle>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />

      <Card title="Dönemler">
        <Async state={terms} isEmpty={(d) => d.length === 0} empty="Dönem yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Dönem</th><th>Kayıt</th><th /></tr></thead>
              <tbody>
                {d.map((x) => (
                  <tr key={x.id}>
                    <td><strong>{x.name}</strong> <code>{x.code}</code></td>
                    <td>{x.registration_open ? <Badge tone="ok">Açık</Badge> : <Badge tone="muted">Kapalı</Badge>}</td>
                    <td><button className="btn small" disabled={act.busy} onClick={() => toggle(x)}>{x.registration_open ? 'Kaydı kapat' : 'Kaydı aç'}</button></td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Async>
        <CreateForm title="Yeni dönem" onSubmit={async () => { await post('/terms', { code: t.code, name: t.name, registration_open: false }); terms.reload(); termState.reload(); setT({ code: '', name: '' }); return 'Dönem oluşturuldu (kayıt kapalı).'; }}>
          <Field label="Kod"><input required value={t.code} onChange={(e) => setT({ ...t, code: e.target.value })} /></Field>
          <Field label="Ad"><input required value={t.name} onChange={(e) => setT({ ...t, name: e.target.value })} /></Field>
        </CreateForm>
        <CreateForm title="Ekle-sil süresi (seçili dönem)" submitLabel="Kaydet" onSubmit={async () => { await put(`/terms/${termId}/drop-deadline`, { deadline: deadline ? new Date(deadline).toISOString() : null }); return deadline ? 'Ekle-sil süresi kaydedildi.' : 'Süre sınırı kaldırıldı.'; }}>
          <Field label="Son gün/saat (boş: sınır yok)"><input type="datetime-local" value={deadline} onChange={(e) => setDeadline(e.target.value)} /></Field>
        </CreateForm>
      </Card>

      <Card title="Şubeler" actions={<Async state={termState}>{() => <TermPicker terms={termList} value={termId} onChange={(id) => { setTermId(id); setShown(25); }} />}</Async>}>
        <Async state={sections} isEmpty={(d) => d.length === 0} empty="Bu dönemde şube yok.">
          {(d) => (
            <>
              <Table>
                <thead><tr><th>Ders</th><th>Şube</th><th className="num">Kontenjan</th><th className="num">Kayıtlı</th><th className="num">Bekleyen</th><th className="num">Öğretim üyesi</th></tr></thead>
                <tbody>{d.slice(0, shown).map((x) => <tr key={x.id}><td><strong>{x.course_code}</strong> <span className="muted">{x.course_name}</span></td><td>{x.code}</td><td className="num">{x.capacity}</td><td className="num">{x.enrolled_count}</td><td className="num">{x.waitlist_size}</td><td className="num">{x.instructor_id ?? '—'}</td></tr>)}</tbody>
              </Table>
              {d.length > shown && <div className="row" style={{ justifyContent: 'center', marginTop: 10 }}><button className="btn" onClick={() => setShown((n) => n + 50)}>Daha fazla ({d.length - shown})</button></div>}
            </>
          )}
        </Async>
        <CreateForm title="Yeni şube" onSubmit={async () => { await post('/sections', { term_id: termId, course_id: Number(s.course), code: s.code, capacity: Number(s.capacity), instructor_id: s.instructor ? Number(s.instructor) : undefined }); sections.reload(); return 'Şube oluşturuldu.'; }}>
          <Field label="Ders"><select required value={s.course} onChange={(e) => setS({ ...s, course: e.target.value })}><option value="">Seçin…</option>{(courses.data ?? []).slice(0, 700).map((c) => <option key={c.id} value={c.id}>{c.code} — {c.name}</option>)}</select></Field>
          <Field label="Şube kodu"><input required value={s.code} onChange={(e) => setS({ ...s, code: e.target.value })} /></Field>
          <Field label="Kontenjan"><input type="number" required min={1} value={s.capacity} onChange={(e) => setS({ ...s, capacity: e.target.value })} /></Field>
          <Field label="Öğretim üyesi (kullanıcı no)"><input type="number" min={1} value={s.instructor} onChange={(e) => setS({ ...s, instructor: e.target.value })} /></Field>
        </CreateForm>
      </Card>

      <Card title="Derslikler">
        <Async state={rooms} isEmpty={(d) => d.length === 0} empty="Derslik yok.">
          {(d) => <Table><thead><tr><th>Kod</th><th>Tür</th><th className="num">Kapasite</th></tr></thead><tbody>{d.map((x) => <tr key={x.id}><td><code>{x.code}</code></td><td>{KIND_LABEL[x.kind] ?? x.kind}</td><td className="num">{x.capacity}</td></tr>)}</tbody></Table>}
        </Async>
        <CreateForm title="Yeni derslik" onSubmit={async () => { await post('/rooms', { code: r.code, capacity: Number(r.capacity), kind: r.kind }); rooms.reload(); setR({ ...r, code: '' }); return 'Derslik oluşturuldu.'; }}>
          <Field label="Kod"><input required value={r.code} onChange={(e) => setR({ ...r, code: e.target.value })} /></Field>
          <Field label="Kapasite"><input type="number" required min={1} value={r.capacity} onChange={(e) => setR({ ...r, capacity: e.target.value })} /></Field>
          <Field label="Tür"><select value={r.kind} onChange={(e) => setR({ ...r, kind: e.target.value })}><option value="lecture">Sınıf</option><option value="lab">Laboratuvar</option><option value="amphi">Amfi</option></select></Field>
        </CreateForm>
      </Card>

      {user?.role === 'registrar' && (
        <Card title="Tekrar alınan derslerde GANO politikası">
          <p className="muted" style={{ marginTop: 0 }}>Politika değişince programdaki <strong>tüm öğrencilerin</strong> GANO'su arka plan işiyle yeniden hesaplanır.</p>
          <CreateForm title="Politika" submitLabel="Uygula" onSubmit={async () => { const x = await put<{ recalculating_students: number }>(`/programs/${pol.program}/retake-policy`, { policy: pol.policy }); return `${x.recalculating_students} öğrencinin GANO'su yeniden hesaplanıyor.`; }}>
            <Field label="Program"><select required value={pol.program} onChange={(e) => setPol({ ...pol, program: e.target.value })}><option value="">Seçin…</option>{(programs.data ?? []).map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
            <Field label="Politika"><select value={pol.policy} onChange={(e) => setPol({ ...pol, policy: e.target.value })}><option value="last">En son alınan not</option><option value="best">En yüksek not</option></select></Field>
          </CreateForm>
        </Card>
      )}
    </div>
  );
}

/* --------------------------------------------------------------- Program çözücü ve sınav takvimi --------------------------------------------------------------- */

interface SolveResult {
  status: 'solved' | 'infeasible' | 'timeout';
  applied: boolean;
  nodes: number;
  restarts: number;
  elapsed_ms: number;
  sections: number;
  rooms: number;
  instructors: number;
  conflicts: { kind: string; message: string }[] | null;
  placements?: { section_id: number; course_code: string; day: number; start_min: number; end_min: number; room: string }[];
}
interface Exam { id: number; course_code: string; kind: string; starts_at: string; duration_min: number; room?: string }
const EXAM_KIND: Record<string, string> = { midterm: 'Vize', final: 'Final', makeup: 'Bütünleme' };

export function Timetable() {
  const { terms, state: termState, termId, setTermId } = useTermSelect();
  const act = useAction();
  const [result, setResult] = useState<SolveResult | null>(null);
  const [limit, setLimit] = useState('30');
  const schedule = useFetch(termId ? () => listAll<{ id: number; course_code: string; day: number; start_min: number; end_min: number; room?: string }>(`/terms/${termId}/schedule`, 3000) : null, [termId]);
  const exams = useFetch(termId ? () => listAll<Exam>(`/terms/${termId}/exams`) : null, [termId]);
  const sections = useFetch(termId ? () => listAll<Sec>(`/sections?term_id=${termId}`, 2000) : null, [termId]);
  const rooms = useFetch(() => listAll<Room>('/rooms'), []);
  const [ex, setEx] = useState({ section: '', kind: 'midterm', at: '', minutes: '90', room: '' });
  useEffect(() => setResult(null), [termId]);

  const solve = (apply: boolean) =>
    act.run(async () => {
      const r = await post<SolveResult>(`/terms/${termId}/schedule/solve`, { apply, time_limit_seconds: Number(limit) });
      setResult(r);
      if (r.applied) {
        schedule.reload();
        return 'Program uygulandı.';
      }
    });

  return (
    <div className="stack">
      <PageTitle sub="Çözücü derslik kapasitesi/türü, öğretim üyesi müsaitliği ve aynı yarıyılın zorunlu derslerinin çakışmamasını gözetir.">Program & Sınav Takvimi</PageTitle>
      <Card
        title="Ders programı çözücü"
        actions={<Async state={termState}>{() => <TermPicker terms={terms} value={termId} onChange={setTermId} />}</Async>}
      >
        <div className="row" style={{ marginBottom: 12 }}>
          <label className="field inline"><span>Süre sınırı (sn)</span><input type="number" min={1} max={60} value={limit} onChange={(e) => setLimit(e.target.value)} /></label>
          <button className="btn" disabled={act.busy || !termId} onClick={() => solve(false)}>{act.busy ? 'Çözülüyor…' : 'Çöz (yalnızca dene)'}</button>
          <button className="btn primary" disabled={act.busy || !termId} onClick={() => { if (window.confirm('Dönemin mevcut ders saatleri çözümle DEĞİŞTİRİLECEK. Devam edilsin mi?')) solve(true); }}>Çöz ve uygula</button>
        </div>
        <Notice error={act.error} ok={act.ok} onClose={act.clear} />
        {result && (
          <>
            <div className={`alert ${result.status === 'solved' ? 'ok' : 'error'}`} role="status">
              <div>
                <strong>{result.status === 'solved' ? 'Çakışmasız program bulundu.' : result.status === 'infeasible' ? 'Bu kısıtlarla çözüm yok.' : 'Süre doldu; çözüm bulunamadı.'}</strong>
                <div className="muted" style={{ color: 'inherit' }}>{result.sections} şube · {result.rooms} derslik · {result.instructors} öğretim üyesi · {result.elapsed_ms} ms · {result.nodes} düğüm</div>
              </div>
              {result.applied && <Badge tone="ok">Uygulandı</Badge>}
            </div>
            {result.conflicts && result.conflicts.length > 0 && (
              <>
                <strong>Çatışan kısıtlar</strong>
                <ul className="violations">{result.conflicts.map((c, i) => <li key={i}><code>{c.kind}</code> {c.message}</li>)}</ul>
                <p className="muted">Not: <code>SEARCH_CONFLICT</code> açıklamaları aramadan türetilmiş yaklaşık nedenlerdir; diğerleri kesin analizdir.</p>
              </>
            )}
            {result.placements && !result.applied && (
              <>
                <strong>Önerilen program (henüz uygulanmadı)</strong>
                <Table>
                  <thead><tr><th>Ders</th><th>Gün</th><th>Saat</th><th>Derslik</th></tr></thead>
                  <tbody>{[...result.placements].sort((a, b) => a.day - b.day || a.start_min - b.start_min).map((p) => <tr key={p.section_id}><td><strong>{p.course_code}</strong></td><td>{DAYS[p.day - 1]}</td><td>{fmtMinutes(p.start_min)}–{fmtMinutes(p.end_min)}</td><td>{p.room}</td></tr>)}</tbody>
                </Table>
              </>
            )}
          </>
        )}
      </Card>

      <Card title="Programı elle düzenle (sürükle-bırak)">
        <Async state={schedule} isEmpty={(d) => d.length === 0} empty="Düzenlenecek program yok. Önce çözücüyü çalıştırın.">
          {(d) => <ScheduleEditor meetings={d} onChanged={schedule.reload} />}
        </Async>
      </Card>

      <Card title="Mevcut ders programı">
        <Async state={schedule} isEmpty={(d) => d.length === 0} empty="Bu dönem için ders saati tanımlı değil. Yukarıdan çözücüyü çalıştırın.">
          {(d) => (
            <Table>
              <thead><tr><th>Gün</th><th>Saat</th><th>Ders</th><th>Derslik</th></tr></thead>
              <tbody>{[...d].sort((a, b) => a.day - b.day || a.start_min - b.start_min).slice(0, 300).map((m) => <tr key={m.id}><td>{DAYS[m.day - 1]}</td><td>{fmtMinutes(m.start_min)}–{fmtMinutes(m.end_min)}</td><td><strong>{m.course_code}</strong></td><td>{m.room || '—'}</td></tr>)}</tbody>
            </Table>
          )}
        </Async>
      </Card>

      <Card title="Sınav takvimi">
        <Async state={exams} isEmpty={(d) => d.length === 0} empty="Bu dönem için planlanmış sınav yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Tarih</th><th>Ders</th><th>Tür</th><th>Süre</th><th>Derslik</th></tr></thead>
              <tbody>{[...d].sort((a, b) => a.starts_at.localeCompare(b.starts_at)).map((x) => <tr key={x.id}><td>{new Date(x.starts_at).toLocaleString('tr-TR', { dateStyle: 'medium', timeStyle: 'short' })}</td><td><strong>{x.course_code}</strong></td><td>{EXAM_KIND[x.kind] ?? x.kind}</td><td>{x.duration_min} dk</td><td>{x.room || '—'}</td></tr>)}</tbody>
            </Table>
          )}
        </Async>
        <CreateForm title="Sınav ekle (oda, öğretim üyesi ve öğrenci çakışmaları reddedilir)" onSubmit={async () => { await post(`/sections/${ex.section}/exams`, { kind: ex.kind, starts_at: new Date(ex.at).toISOString(), duration_min: Number(ex.minutes), room: ex.room || undefined }); exams.reload(); return 'Sınav eklendi.'; }}>
          <Field label="Şube"><select required value={ex.section} onChange={(e) => setEx({ ...ex, section: e.target.value })}><option value="">Seçin…</option>{(sections.data ?? []).slice(0, 700).map((s) => <option key={s.id} value={s.id}>{s.course_code} · şube {s.code}</option>)}</select></Field>
          <Field label="Tür"><select value={ex.kind} onChange={(e) => setEx({ ...ex, kind: e.target.value })}><option value="midterm">Vize</option><option value="final">Final</option><option value="makeup">Bütünleme</option></select></Field>
          <Field label="Başlangıç"><input type="datetime-local" required value={ex.at} onChange={(e) => setEx({ ...ex, at: e.target.value })} /></Field>
          <Field label="Süre (dk)"><input type="number" min={15} max={300} required value={ex.minutes} onChange={(e) => setEx({ ...ex, minutes: e.target.value })} /></Field>
          <Field label="Derslik"><select value={ex.room} onChange={(e) => setEx({ ...ex, room: e.target.value })}><option value="">(belirtme)</option>{(rooms.data ?? []).map((r) => <option key={r.id} value={r.code}>{r.code}</option>)}</select></Field>
        </CreateForm>
      </Card>
    </div>
  );
}

/* --------------------------------------------------------------- Not düzeltme onayı --------------------------------------------------------------- */

interface Correction { id: number; result_id: number; requested_by: number; old_letter: string; new_letter: string; reason: string; status: string; course_code: string; student_no: string }

export function Corrections() {
  const list = useFetch(() => listAll<Correction>('/grade-corrections?status=pending'), []);
  const act = useAction();
  const decide = (c: Correction, approve: boolean) =>
    act.run(async () => {
      await post(`/grade-corrections/${c.id}/decision`, { approve });
      list.reload();
      return approve ? `${c.student_no} · ${c.course_code}: not ${c.old_letter} → ${c.new_letter} olarak değiştirildi.` : 'Talep reddedildi.';
    });
  return (
    <div className="stack">
      <PageTitle sub="Geçmişe dönük not değişikliği çift onay ister: talebi öğretim üyesi açar, siz ikinci kişi olarak karar verirsiniz. Onay, eski/yeni notla değiştirilemez geçmişe yazılır.">Not Düzeltme Onayları</PageTitle>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
      <Card title="Bekleyen talepler">
        <Async state={list} isEmpty={(d) => d.length === 0} empty="Onay bekleyen düzeltme talebi yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Öğrenci no</th><th>Ders</th><th>Değişiklik</th><th>Gerekçe</th><th /></tr></thead>
              <tbody>
                {d.map((c) => (
                  <tr key={c.id}>
                    <td>{c.student_no}</td>
                    <td><strong>{c.course_code}</strong></td>
                    <td><Badge tone="muted">{c.old_letter}</Badge> → <Badge tone="info">{c.new_letter}</Badge></td>
                    <td>{c.reason}</td>
                    <td><div className="row"><button className="btn small primary" disabled={act.busy} onClick={() => decide(c, true)}>Onayla</button><button className="btn small danger" disabled={act.busy} onClick={() => decide(c, false)}>Reddet</button></div></td>
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

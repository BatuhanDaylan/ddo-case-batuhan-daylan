import { useMemo, useState, type FormEvent } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, del, downloadBlob, get, listAll, newKey, post } from '../api';
import { useAuth } from '../auth';
import { Async, Badge, Card, DAYS, LETTER_TONE, Notice, PageTitle, Table, TermPicker, fmtMinutes, useAction, useFetch } from '../ui';
import { useTermSelect, type Enr, type Sec } from './shared';

/* ------------------------------------------------------------------ Ders Kaydı ------------------------------------------------------------------ */

interface Validation {
  valid: boolean;
  violations: { code: string; message: string }[];
  total_ects: number;
  max_ects: number;
  gano: number;
}

const VIOLATION_LABEL: Record<string, string> = {
  PREREQ_NOT_MET: 'Ön koşul',
  TIME_CONFLICT: 'Saat çakışması',
  DUPLICATE_COURSE: 'Aynı ders',
  ALREADY_PASSED: 'Zaten geçilmiş',
  ECTS_LIMIT_EXCEEDED: 'AKTS sınırı',
};

function statusBadge(e: Enr) {
  if (e.status === 'waitlisted') return <Badge tone="warn">Bekleme #{e.waitlist_position}</Badge>;
  return <Badge tone="ok">Kayıtlı</Badge>;
}

const advisorBadge = (s: Enr['advisor_status']) =>
  s === 'approved' ? <Badge tone="ok">Danışman onayladı</Badge> : s === 'rejected' ? <Badge tone="bad">Reddedildi</Badge> : <Badge tone="muted">Onay bekliyor</Badge>;

export function Enroll() {
  const { terms, state: termState, termId, setTermId } = useTermSelect();
  const [q, setQ] = useState('');
  const [shown, setShown] = useState(50);
  const [cart, setCart] = useState<Set<number>>(new Set());
  const [validation, setValidation] = useState<Validation | null>(null);
  const act = useAction();
  const vAct = useAction();

  const sections = useFetch(termId ? () => listAll<Sec>(`/sections?term_id=${termId}`, 1500) : null, [termId]);
  const mine = useFetch(() => listAll<Enr>('/me/enrollments'), []);
  const mineBySection = useMemo(() => new Map((mine.data ?? []).map((e) => [e.section_id, e])), [mine.data]);

  const filtered = useMemo(() => {
    const s = (sections.data ?? []).filter((x) => `${x.course_code} ${x.course_name}`.toLowerCase().includes(q.toLowerCase()));
    return s;
  }, [sections.data, q]);

  const open = terms.find((t) => t.id === termId)?.registration_open;

  function toggle(id: number) {
    setValidation(null);
    setCart((c) => {
      const n = new Set(c);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  }

  const enroll = (s: Sec) =>
    act.run(async () => {
      const r = await post<{ status: string; waitlist_position?: number }>(`/sections/${s.id}/enrollments`, {}, { 'Idempotency-Key': newKey() });
      mine.reload();
      sections.reload();
      return r.status === 'enrolled' ? `${s.course_code} dersine kaydoldunuz.` : `${s.course_code} dolu: bekleme listesine alındınız (sıra ${r.waitlist_position}).`;
    });

  const drop = (e: Enr) =>
    act.run(async () => {
      await del(`/enrollments/${e.id}`);
      mine.reload();
      sections.reload();
      return `${e.course_code} kaydı bırakıldı.`;
    });

  const validate = () =>
    vAct.run(async () => {
      setValidation(await post<Validation>('/me/enrollments/validate', { section_ids: [...cart] }));
    });

  return (
    <div className="stack">
      <PageTitle sub="Kontenjan dolunca bekleme listesine alınırsınız; koltuk boşalınca sırayla otomatik atanırsınız.">Ders Kaydı</PageTitle>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />

      <Card
        title="Şubeler"
        actions={
          <>
            <Async state={termState}>{() => <TermPicker terms={terms} value={termId} onChange={(id) => { setTermId(id); setCart(new Set()); setValidation(null); }} />}</Async>
            <input type="search" placeholder="Ders ara (kod/ad)…" aria-label="Ders ara" value={q} onChange={(e) => { setQ(e.target.value); setShown(50); }} />
          </>
        }
      >
        {open === false && <div className="alert info" role="status">Bu dönem için kayıt kapalı.</div>}
        <Async state={sections} isEmpty={(d) => d.length === 0} empty="Bu dönem için şube bulunamadı.">
          {() =>
            filtered.length === 0 ? (
              <div className="empty">Aramanızla eşleşen şube yok.</div>
            ) : (
              <>
                <Table>
                  <thead>
                    <tr><th aria-label="Sepet" /><th>Ders</th><th className="hide-sm">Şube</th><th className="num hide-sm">AKTS</th><th className="hide-sm">Doluluk</th><th className="num hide-sm">Bekleyen</th><th>Durumum</th><th /></tr>
                  </thead>
                  <tbody>
                    {filtered.slice(0, shown).map((s) => {
                      const m = mineBySection.get(s.id);
                      const full = s.enrolled_count >= s.capacity;
                      return (
                        <tr key={s.id}>
                          <td><input type="checkbox" aria-label={`${s.course_code} sepete ekle`} checked={cart.has(s.id)} onChange={() => toggle(s.id)} /></td>
                          <td>
                            <strong>{s.course_code}</strong> <span className="muted">{s.course_name}</span>
                            <small className="muted show-sm">Şube {s.code} · {s.ects} AKTS · {s.enrolled_count}/{s.capacity} kişi{s.waitlist_size > 0 ? `, ${s.waitlist_size} bekleyen` : ''}</small>
                          </td>
                          <td className="hide-sm">{s.code}</td>
                          <td className="num hide-sm">{s.ects}</td>
                          <td className="hide-sm" style={{ minWidth: 130 }}>
                            <div className="bar" title={`${s.enrolled_count}/${s.capacity}`}><i style={{ width: `${Math.min(100, (s.enrolled_count / s.capacity) * 100)}%` }} /></div>
                            <small className="muted">{s.enrolled_count}/{s.capacity} {full && <Badge tone="warn">Dolu</Badge>}</small>
                          </td>
                          <td className="num hide-sm">{s.waitlist_size}</td>
                          <td>{m ? statusBadge(m) : <span className="muted">—</span>}</td>
                          <td>
                            {m ? (
                              <button className="btn small danger" disabled={act.busy} onClick={() => drop(m)}>Bırak</button>
                            ) : (
                              <button className="btn small primary" disabled={act.busy || open === false} onClick={() => enroll(s)}>{full ? 'Beklemeye gir' : 'Kayıt ol'}</button>
                            )}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </Table>
                {filtered.length > shown && (
                  <div className="row" style={{ justifyContent: 'center', marginTop: 12 }}>
                    <button className="btn" onClick={() => setShown((n) => n + 50)}>Daha fazla göster ({filtered.length - shown} şube daha)</button>
                  </div>
                )}
              </>
            )
          }
        </Async>
      </Card>

      <Card
        title={`Kural denetimi (${cart.size} şube seçili)`}
        actions={<button className="btn primary" disabled={cart.size === 0 || vAct.busy} onClick={validate}>{vAct.busy ? 'Denetleniyor…' : 'Kuralları denetle'}</button>}
      >
        <p className="muted" style={{ marginTop: 0 }}>Şubeleri işaretleyip denetleyin: ön koşul, saat çakışması, aynı ders ve AKTS sınırı ihlalleri <strong>hepsi birlikte</strong> listelenir. Hiçbir şey kaydedilmez.</p>
        <Notice error={vAct.error} />
        {validation && (
          <>
            <div className={`alert ${validation.valid ? 'ok' : 'error'}`} role="status">
              <strong>{validation.valid ? 'Seçilen şubeler kurallara uygun.' : `${validation.violations.length} ihlal bulundu.`}</strong>
              <span>AKTS: {validation.total_ects} / {validation.max_ects} (GANO {validation.gano.toFixed(2)})</span>
            </div>
            {validation.violations.length > 0 && (
              <ul className="violations">
                {validation.violations.map((v, i) => (
                  <li key={i}><Badge tone="bad">{VIOLATION_LABEL[v.code] ?? v.code}</Badge> {v.message}</li>
                ))}
              </ul>
            )}
          </>
        )}
      </Card>

      <Card title="Kayıtlarım">
        <Async state={mine} isEmpty={(d) => d.length === 0} empty="Henüz bir derse kayıtlı değilsiniz.">
          {(d) => (
            <Table>
              <thead><tr><th>Ders</th><th className="num">AKTS</th><th>Durum</th><th>Danışman</th><th /></tr></thead>
              <tbody>
                {d.map((e) => (
                  <tr key={e.id}>
                    <td><strong>{e.course_code}</strong> <span className="muted">{e.course_name}</span></td>
                    <td className="num">{e.ects}</td>
                    <td>{statusBadge(e)}</td>
                    <td>{advisorBadge(e.advisor_status)}</td>
                    <td><button className="btn small danger" disabled={act.busy} onClick={() => drop(e)}>Bırak</button></td>
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

/* ------------------------------------------------------------------ Program ------------------------------------------------------------------ */

interface Meeting { id: number; section_id: number; course_code: string; day: number; start_min: number; end_min: number; room?: string }
interface Exam { id: number; course_code: string; kind: string; starts_at: string; duration_min: number; room?: string }
const EXAM_KIND: Record<string, string> = { midterm: 'Vize', final: 'Final', makeup: 'Bütünleme' };

export function Schedule() {
  const { terms, state: termState, termId, setTermId } = useTermSelect();
  const mine = useFetch(() => listAll<Enr>('/me/enrollments'), []);
  const meetings = useFetch(termId ? () => listAll<Meeting>(`/terms/${termId}/schedule`, 4000) : null, [termId]);
  const exams = useFetch(() => get<{ items: Exam[] }>('/me/exam-schedule'), []);

  const mySections = useMemo(() => new Set((mine.data ?? []).filter((e) => e.status === 'enrolled').map((e) => e.section_id)), [mine.data]);
  const items = (meetings.data ?? []).filter((m) => mySections.has(m.section_id));
  const hours = Array.from({ length: 10 }, (_, i) => 8 + i);

  return (
    <div className="stack">
      <PageTitle sub="Yalnızca kayıtlı olduğunuz şubeler gösterilir.">Haftalık Program</PageTitle>
      <Card title="Ders programı" actions={<Async state={termState}>{() => <TermPicker terms={terms} value={termId} onChange={setTermId} />}</Async>}>
        <Async state={meetings}>
          {() =>
            items.length === 0 ? (
              <div className="empty">Bu dönem için programınızda ders saati yok. (Kayıtlı olduğunuz şubelerin saatleri henüz planlanmamış olabilir.)</div>
            ) : (
              <div className="table-wrap">
                <table className="timetable">
                  <thead><tr><th style={{ width: 64 }}>Saat</th>{DAYS.slice(0, 5).map((d) => <th key={d}>{d}</th>)}</tr></thead>
                  <tbody>
                    {hours.map((h) => (
                      <tr key={h}>
                        <th>{fmtMinutes(h * 60)}</th>
                        {[1, 2, 3, 4, 5].map((d) => {
                          const m = items.find((x) => x.day === d && x.start_min <= h * 60 && x.end_min > h * 60);
                          return (
                            <td key={d}>
                              {m && m.start_min <= h * 60 && h * 60 < m.start_min + 60 && (
                                <div className="slot"><strong>{m.course_code}</strong><br />{fmtMinutes(m.start_min)}–{fmtMinutes(m.end_min)}{m.room ? ` · ${m.room}` : ''}</div>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          }
        </Async>
      </Card>
      <Card title="Sınav takvimim">
        <Async state={exams} isEmpty={(d) => d.items.length === 0} empty="Henüz planlanmış sınavınız yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Tarih</th><th>Ders</th><th>Tür</th><th>Süre</th><th>Derslik</th></tr></thead>
              <tbody>
                {d.items.map((x) => (
                  <tr key={x.id}>
                    <td>{new Date(x.starts_at).toLocaleString('tr-TR', { dateStyle: 'medium', timeStyle: 'short' })}</td>
                    <td><strong>{x.course_code}</strong></td>
                    <td>{EXAM_KIND[x.kind] ?? x.kind}</td>
                    <td>{x.duration_min} dk</td>
                    <td>{x.room || '—'}</td>
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

/* ------------------------------------------------------------------ Notlarım ------------------------------------------------------------------ */

interface ResultRow { result_id: number; course_code: string; course_name: string; ects: number; term: string; letter: string; grade_point: number; passed: boolean; exempt: boolean }
interface ResultsResp { gano: number; gano_cached: number; results: ResultRow[] }
interface Objection { id: number; course_code: string; letter: string; reason: string; status: 'open' | 'accepted' | 'rejected'; response: string | null }
interface Issued { id: number; code: string; verify_url: string; pdf_url: string }

export function Grades() {
  const { user } = useAuth();
  const sid = user?.student_id;
  const results = useFetch(sid ? () => get<ResultsResp>(`/students/${sid}/results`) : null, [sid]);
  const objections = useFetch(() => listAll<Objection>('/me/objections'), []);
  const [objecting, setObjecting] = useState<number | null>(null);
  const [reason, setReason] = useState('');
  const [doc, setDoc] = useState<Issued | null>(null);
  const act = useAction();

  const issue = () =>
    act.run(async () => {
      const d = await post<Issued>(`/students/${sid}/transcripts`);
      setDoc(d);
      const blob = await downloadBlob(d.pdf_url);
      window.open(URL.createObjectURL(blob), '_blank', 'noopener');
      return 'Transkript düzenlendi ve imzalandı. PDF yeni sekmede açıldı.';
    });

  const submitObjection = (e: FormEvent) => {
    e.preventDefault();
    return act.run(async () => {
      await post(`/course-results/${objecting}/objections`, { reason });
      setObjecting(null);
      setReason('');
      objections.reload();
      return 'İtirazınız öğretim üyesine iletildi.';
    });
  };

  return (
    <div className="stack">
      <PageTitle sub="Notlarınız, ortalamanız ve imzalı transkriptiniz.">Notlarım</PageTitle>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
      <Async state={results} isEmpty={(d) => d.results.length === 0} empty="Henüz ilan edilmiş bir notunuz yok.">
        {(d) => (
          <>
            <div className="grid cols-3">
              <Card><div className="stat"><b className="big">{d.gano.toFixed(2)}</b><span>GANO (anlık, kaynak veriden hesaplanır)</span></div></Card>
              <Card><div className="stat"><b>{d.results.filter((r) => r.passed).reduce((s, r) => s + r.ects, 0)}</b><span>başarılan AKTS</span></div></Card>
              <Card>
                <div className="stat">
                  <b style={{ fontSize: '1rem' }}><button className="btn primary" disabled={act.busy} onClick={issue}>{act.busy ? 'Hazırlanıyor…' : 'Transkript PDF’i oluştur'}</button></b>
                  <span>HMAC imzalı, QR kodlu; herkese açık sayfadan doğrulanır</span>
                </div>
              </Card>
            </div>
            {doc && (
              <div className="alert info" role="status">
                <span>Son belge: <Link to={`/verify/${doc.code}`}>doğrulama sayfası</Link> · kod <code>{doc.code.slice(0, 14)}…</code></span>
                <button className="btn small" onClick={async () => { const b = await downloadBlob(doc.pdf_url); window.open(URL.createObjectURL(b), '_blank', 'noopener'); }}>PDF’i tekrar aç</button>
              </div>
            )}
            <Card title="Ders sonuçları">
              <Table>
                <thead><tr><th>Dönem</th><th>Ders</th><th className="num">AKTS</th><th>Not</th><th className="num">Katsayı</th><th /></tr></thead>
                <tbody>
                  {d.results.map((r) => (
                    <tr key={r.result_id}>
                      <td>{r.term}</td>
                      <td><strong>{r.course_code}</strong> <span className="muted">{r.course_name}</span></td>
                      <td className="num">{r.ects}</td>
                      <td>{r.exempt ? <Badge tone="info">Muaf</Badge> : <Badge tone={LETTER_TONE(r.letter)}>{r.letter}</Badge>}</td>
                      <td className="num">{r.exempt ? '—' : r.grade_point.toFixed(1)}</td>
                      <td>{!r.exempt && <button className="btn small" onClick={() => setObjecting(r.result_id)}>İtiraz et</button>}</td>
                    </tr>
                  ))}
                </tbody>
              </Table>
              {objecting && (
                <form className="form" onSubmit={submitObjection} style={{ marginTop: 12 }}>
                  <label className="field"><span>İtiraz gerekçesi</span><textarea required rows={3} value={reason} onChange={(e) => setReason(e.target.value)} /></label>
                  <div className="row">
                    <button className="btn primary" disabled={act.busy}>İtirazı gönder</button>
                    <button type="button" className="btn" onClick={() => setObjecting(null)}>Vazgeç</button>
                  </div>
                </form>
              )}
            </Card>
          </>
        )}
      </Async>
      <Card title="İtirazlarım">
        <Async state={objections} isEmpty={(d) => d.length === 0} empty="Açık ya da sonuçlanmış itirazınız yok.">
          {(d) => (
            <Table>
              <thead><tr><th>Ders</th><th>Not</th><th>Gerekçe</th><th>Durum</th><th>Yanıt</th></tr></thead>
              <tbody>
                {d.map((o) => (
                  <tr key={o.id}>
                    <td><strong>{o.course_code}</strong></td>
                    <td>{o.letter}</td>
                    <td>{o.reason}</td>
                    <td><Badge tone={o.status === 'accepted' ? 'ok' : o.status === 'rejected' ? 'bad' : 'warn'}>{o.status === 'open' ? 'İnceleniyor' : o.status === 'accepted' ? 'Kabul edildi' : 'Reddedildi'}</Badge></td>
                    <td>{o.response ?? '—'}{o.status === 'accepted' && <div className="muted">Not değişikliği ikinci onay bekler.</div>}</td>
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

/* ------------------------------------------------------------------ Mezuniyet ------------------------------------------------------------------ */

interface Audit {
  report: {
    can_graduate: boolean; gano: number; min_gano: number; gano_ok: boolean; total_ects: number; min_total_ects: number; ects_ok: boolean;
    required_missing: string[]; required_ok: boolean; missing: string[];
    pools: { name: string; min_courses: number; min_ects: number; assigned: string[]; assigned_ects: number; missing_courses: number; missing_ects: number; ok: boolean; remaining_candidates: string[] }[];
    internship?: { course: string; ok: boolean };
  };
  remaining_required_courses: { code: string; name: string; ects: number; semester?: number }[];
}

export function Graduation() {
  const { user } = useAuth();
  const sid = user?.student_id;
  const audit = useFetch<Audit | 'norules'>(
    sid
      ? () =>
          get<Audit>(`/students/${sid}/graduation-audit`).catch((e) => {
            if (e instanceof ApiError && (e.code === 'no_rules' || e.code === 'no_curriculum')) return 'norules' as const;
            throw e;
          })
      : null,
    [sid],
  );
  return (
    <div className="stack">
      <PageTitle sub="Giriş yılınızdaki müfredatın kurallarına göre denetlenir.">Mezuniyet Durumu</PageTitle>
      <Async state={audit}>
        {(a) =>
          a === 'norules' ? (
            <div className="empty">Müfredatınız için mezuniyet kuralı henüz tanımlanmamış.</div>
          ) : (
            <>
              <div className={`alert ${a.report.can_graduate ? 'ok' : 'info'}`} role="status">
                <strong>{a.report.can_graduate ? 'Mezuniyet şartlarının hepsini sağlıyorsunuz.' : 'Mezuniyet için eksikleriniz var:'}</strong>
              </div>
              {!a.report.can_graduate && (
                <Card title="Eksikler">
                  <ul className="violations">{a.report.missing.map((m, i) => <li key={i}>{m}</li>)}</ul>
                </Card>
              )}
              <div className="grid cols-3">
                <Card><div className="stat"><b>{a.report.gano.toFixed(2)} <Badge tone={a.report.gano_ok ? 'ok' : 'bad'}>{a.report.gano_ok ? 'Yeterli' : `en az ${a.report.min_gano.toFixed(2)}`}</Badge></b><span>GANO</span></div></Card>
                <Card><div className="stat"><b>{a.report.total_ects} / {a.report.min_total_ects}</b><span>toplam AKTS</span><div className={`bar ${a.report.ects_ok ? 'ok' : ''}`}><i style={{ width: `${Math.min(100, a.report.min_total_ects ? (a.report.total_ects / a.report.min_total_ects) * 100 : 100)}%` }} /></div></div></Card>
                <Card><div className="stat"><b>{a.report.required_ok ? 'Tamam' : `${a.report.required_missing.length} eksik`}</b><span>zorunlu dersler</span></div></Card>
              </div>
              <Card title="Kalan zorunlu dersler">
                {a.remaining_required_courses.length === 0 ? (
                  <div className="empty">Tüm zorunlu dersleri tamamladınız.</div>
                ) : (
                  <Table>
                    <thead><tr><th>Ders</th><th className="num">AKTS</th><th className="num">Yarıyıl</th></tr></thead>
                    <tbody>{a.remaining_required_courses.map((c) => <tr key={c.code}><td><strong>{c.code}</strong> <span className="muted">{c.name}</span></td><td className="num">{c.ects}</td><td className="num">{c.semester ?? '—'}</td></tr>)}</tbody>
                  </Table>
                )}
              </Card>
              {a.report.pools.length > 0 && (
                <Card title="Seçmeli havuzlar">
                  <p className="muted" style={{ marginTop: 0 }}>Bir ders birden çok havuzda geçerli olsa da yalnızca birine sayılır; sistem sizin için <strong>en uygun atamayı</strong> bulur.</p>
                  <Table>
                    <thead><tr><th>Havuz</th><th>Şart</th><th>Sayılan dersler</th><th>Durum</th></tr></thead>
                    <tbody>
                      {a.report.pools.map((p) => (
                        <tr key={p.name}>
                          <td><strong>{p.name}</strong></td>
                          <td>{p.min_courses > 0 && `${p.min_courses} ders`}{p.min_courses > 0 && p.min_ects > 0 && ' · '}{p.min_ects > 0 && `${p.min_ects} AKTS`}</td>
                          <td>{p.assigned.length ? p.assigned.join(', ') : '—'} <span className="muted">({p.assigned_ects} AKTS)</span></td>
                          <td>{p.ok ? <Badge tone="ok">Tamam</Badge> : <Badge tone="warn">{p.missing_courses} ders / {p.missing_ects} AKTS eksik</Badge>}{!p.ok && p.remaining_candidates.length > 0 && <div className="muted">Alabilirsiniz: {p.remaining_candidates.join(', ')}</div>}</td>
                        </tr>
                      ))}
                    </tbody>
                  </Table>
                </Card>
              )}
              {a.report.internship && <div className={`alert ${a.report.internship.ok ? 'ok' : 'info'}`}>Staj dersi ({a.report.internship.course}): {a.report.internship.ok ? 'tamamlandı' : 'tamamlanmadı'}</div>}
            </>
          )
        }
      </Async>
    </div>
  );
}

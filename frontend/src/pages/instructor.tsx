import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Link, useParams } from 'react-router-dom';
import { get, listAll, post, put } from '../api';
import { useAuth } from '../auth';
import { Async, Badge, Card, LETTER_TONE, Notice, PageTitle, Table, TermPicker, useAction, useFetch } from '../ui';
import { useTermSelect, type Sec } from './shared';

const LETTERS = ['AA', 'BA', 'BB', 'CB', 'CC', 'DC', 'DD', 'FF'];

export function MySections() {
  const { user } = useAuth();
  const { terms, state: termState, termId, setTermId } = useTermSelect();
  const secs = useFetch(termId ? () => listAll<Sec>(`/sections?term_id=${termId}`, 3000) : null, [termId]);
  const mine = (secs.data ?? []).filter((s) => s.instructor_id === user?.id);
  return (
    <div className="stack">
      <PageTitle sub="Yalnızca sizin yürüttüğünüz şubeler listelenir.">Şubelerim</PageTitle>
      <Card title="Şubeler" actions={<Async state={termState}>{() => <TermPicker terms={terms} value={termId} onChange={setTermId} />}</Async>}>
        <Async state={secs}>
          {() =>
            mine.length === 0 ? (
              <div className="empty">Bu dönemde size atanmış şube yok.</div>
            ) : (
              <Table>
                <thead><tr><th>Ders</th><th>Şube</th><th className="num">Kayıtlı</th><th className="num">Bekleyen</th><th /></tr></thead>
                <tbody>
                  {mine.map((s) => (
                    <tr key={s.id}>
                      <td><strong>{s.course_code}</strong> <span className="muted">{s.course_name}</span></td>
                      <td>{s.code}</td>
                      <td className="num">{s.enrolled_count}/{s.capacity}</td>
                      <td className="num">{s.waitlist_size}</td>
                      <td><Link className="btn small primary" to={`/sections/${s.id}`}>Aç</Link></td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )
          }
        </Async>
      </Card>
    </div>
  );
}

/* ----------------------------------------------------------------------------------------------------------------------------- */

interface Grading { method: 'absolute' | 'relative'; published: boolean; components: { name: string; weight: number }[] }
interface RosterRow { enrollment_id: number; student_id: number; student_no: string; full_name: string; scores: { component: string; kind: string; score: number }[] }
interface ResultItem { result_id: number; enrollment_id: number; student_id: number; letter: string }
interface Objection { id: number; result_id: number; student_no: string; course_code: string; letter: string; reason: string; status: 'open' | 'accepted' | 'rejected'; response: string | null }

const COMP_LABEL: Record<string, string> = { midterm: 'Vize', final: 'Final', homework: 'Ödev' };
const KIND_LABEL: Record<string, string> = { regular: '', makeup: ' (bütünleme)', excuse: ' (mazeret)' };

export function SectionDetail() {
  const { id } = useParams();
  const sid = Number(id);
  const sec = useFetch(() => get<Sec>(`/sections/${sid}`), [sid]);
  const grading = useFetch(() => get<Grading>(`/sections/${sid}/grading`), [sid]);
  const roster = useFetch(() => listAll<RosterRow>(`/sections/${sid}/roster`), [sid]);
  const results = useFetch(() => get<{ items: ResultItem[] }>(`/sections/${sid}/results`), [sid]);
  const objections = useFetch(() => listAll<Objection>(`/sections/${sid}/objections`), [sid]);

  const published = grading.data?.published ?? false;
  const reloadAll = () => {
    grading.reload();
    roster.reload();
    results.reload();
    objections.reload();
  };

  return (
    <div className="stack">
      <p style={{ margin: 0 }}><Link to="/sections">← Şubelerim</Link></p>
      <Async state={sec}>
        {(s) => (
          <PageTitle sub={`${s.course_name} · şube ${s.code} · ${s.enrolled_count}/${s.capacity} kayıtlı`}>
            {s.course_code} {published && <Badge tone="info">Notlar ilan edildi</Badge>}
          </PageTitle>
        )}
      </Async>
      <GradingConfig sid={sid} grading={grading} onSaved={reloadAll} />
      {!published && <ScoreEntry sid={sid} grading={grading} roster={roster} onChanged={reloadAll} />}
      {published && <PublishedResults roster={roster} results={results} onChanged={reloadAll} />}
      <ObjectionsPanel objections={objections} onChanged={reloadAll} />
    </div>
  );
}

function GradingConfig({ sid, grading, onSaved }: { sid: number; grading: ReturnType<typeof useFetch<Grading>>; onSaved: () => void }) {
  const [method, setMethod] = useState<'absolute' | 'relative'>('absolute');
  const [w, setW] = useState<Record<string, string>>({ midterm: '', final: '', homework: '' });
  const act = useAction();
  const g = grading.data;

  useEffect(() => {
    if (!g) return;
    setMethod(g.method);
    const next: Record<string, string> = { midterm: '', final: '', homework: '' };
    g.components.forEach((c) => (next[c.name] = String(c.weight)));
    setW(next);
  }, [g]);

  const total = Object.values(w).reduce((s, v) => s + (Number(v) || 0), 0);
  const save = (e: FormEvent) => {
    e.preventDefault();
    return act.run(async () => {
      const components = Object.entries(w).filter(([, v]) => Number(v) > 0).map(([name, v]) => ({ name, weight: Number(v) }));
      await put(`/sections/${sid}/grade-components`, { method, components });
      onSaved();
      return 'Not yapılandırması kaydedildi.';
    });
  };

  return (
    <Card title="Not yapılandırması">
      <Async state={grading}>
        {(d) => (
          <form className="form" onSubmit={save}>
            <div className="form cols">
              <label className="field">
                <span>Harf notu yöntemi</span>
                <select disabled={d.published} value={method} onChange={(e) => setMethod(e.target.value as 'absolute' | 'relative')}>
                  <option value="absolute">Mutlak (sabit eşikler)</option>
                  <option value="relative">Bağıl (T-skoru: ortalama ve standart sapma)</option>
                </select>
              </label>
              {(['midterm', 'final', 'homework'] as const).map((c) => (
                <label className="field" key={c}>
                  <span>{COMP_LABEL[c]} ağırlığı (%)</span>
                  <input type="number" min={0} max={100} disabled={d.published} value={w[c]} onChange={(e) => setW({ ...w, [c]: e.target.value })} />
                </label>
              ))}
            </div>
            <div className="row">
              <Badge tone={total === 100 ? 'ok' : 'warn'}>Toplam %{total}</Badge>
              {!d.published && <button className="btn primary" disabled={act.busy || total !== 100}>Kaydet</button>}
              {d.published && <span className="muted">Notlar ilan edildi; yapılandırma kilitli.</span>}
            </div>
            <Notice error={act.error} ok={act.ok} onClose={act.clear} />
          </form>
        )}
      </Async>
    </Card>
  );
}

function ScoreEntry({ sid, grading, roster, onChanged }: { sid: number; grading: ReturnType<typeof useFetch<Grading>>; roster: ReturnType<typeof useFetch<RosterRow[]>>; onChanged: () => void }) {
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [extra, setExtra] = useState(false);
  const act = useAction();
  const pub = useAction();

  const comps = grading.data?.components ?? [];
  const cols = useMemo(() => {
    const out: { comp: string; kind: string }[] = [];
    for (const c of comps) {
      out.push({ comp: c.name, kind: 'regular' });
      if (extra) {
        if (c.name === 'final') out.push({ comp: c.name, kind: 'makeup' });
        out.push({ comp: c.name, kind: 'excuse' });
      }
    }
    return out;
  }, [comps, extra]);

  const key = (e: number, c: string, k: string) => `${e}|${c}|${k}`;
  const existing = (r: RosterRow, c: string, k: string) => r.scores.find((s) => s.component === c && s.kind === k)?.score;

  const save = () =>
    act.run(async () => {
      const entries = Object.entries(edits)
        .filter(([, v]) => v !== '')
        .map(([k, v]) => {
          const [enrollment_id, component, kind] = k.split('|');
          return { enrollment_id: Number(enrollment_id), component, kind, score: Number(v) };
        });
      if (entries.length === 0) return 'Kaydedilecek değişiklik yok.';
      await put(`/sections/${sid}/scores`, { entries });
      setEdits({});
      onChanged();
      return `${entries.length} puan kaydedildi.`;
    });

  const publish = () =>
    pub.run(async () => {
      if (!window.confirm('Notları ilan ediyorsunuz. İlandan sonra puan girişi kilitlenir ve değişiklik yalnızca ikinci kişi onaylı düzeltme talebiyle yapılabilir. Devam edilsin mi?')) return;
      await post(`/sections/${sid}/grades/publish`);
      onChanged();
      return 'Notlar ilan edildi.';
    });

  return (
    <Card
      title="Puan girişi"
      actions={
        <>
          <label className="row" style={{ fontSize: '0.85rem' }}><input type="checkbox" checked={extra} onChange={(e) => setExtra(e.target.checked)} /> Bütünleme / mazeret sütunları</label>
          <button className="btn" disabled={act.busy || Object.keys(edits).length === 0} onClick={save}>Puanları kaydet</button>
          <button className="btn primary" disabled={pub.busy || comps.length === 0} onClick={publish}>Notları ilan et</button>
        </>
      }
    >
      <p className="muted" style={{ marginTop: 0 }}>Bütünleme/mazeret puanı varsa normal puanın yerine geçer (öncelik: bütünleme › mazeret › normal). Girilmeyen bileşen 0 sayılır.</p>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
      <Notice error={pub.error} ok={pub.ok} onClose={pub.clear} />
      {comps.length === 0 && <div className="alert info">Önce yukarıdan not bileşenlerini (ağırlıkları) tanımlayın.</div>}
      <Async state={roster} isEmpty={(d) => d.length === 0} empty="Bu şubeye kayıtlı öğrenci yok.">
        {(rows) => (
          <Table>
            <thead>
              <tr><th>Öğrenci no</th><th>Ad soyad</th>{cols.map((c) => <th key={`${c.comp}${c.kind}`} className="num">{COMP_LABEL[c.comp]}{KIND_LABEL[c.kind]}</th>)}</tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.enrollment_id}>
                  <td>{r.student_no}</td>
                  <td>{r.full_name}</td>
                  {cols.map((c) => {
                    const k = key(r.enrollment_id, c.comp, c.kind);
                    const ex = existing(r, c.comp, c.kind);
                    return (
                      <td key={k} className="num">
                        <input
                          type="number" min={0} max={100} step="0.5" aria-label={`${r.student_no} ${COMP_LABEL[c.comp]}${KIND_LABEL[c.kind]}`}
                          value={edits[k] ?? (ex !== undefined ? String(ex) : '')}
                          onChange={(e) => setEdits({ ...edits, [k]: e.target.value })}
                        />
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Async>
    </Card>
  );
}

function PublishedResults({ roster, results, onChanged }: { roster: ReturnType<typeof useFetch<RosterRow[]>>; results: ReturnType<typeof useFetch<{ items: ResultItem[] }>>; onChanged: () => void }) {
  const [target, setTarget] = useState<ResultItem | null>(null);
  const [letter, setLetter] = useState('BB');
  const [reason, setReason] = useState('');
  const act = useAction();
  const byEnr = new Map((roster.data ?? []).map((r) => [r.enrollment_id, r]));

  const submit = (e: FormEvent) => {
    e.preventDefault();
    return act.run(async () => {
      await post(`/course-results/${target!.result_id}/corrections`, { new_letter: letter, reason });
      setTarget(null);
      setReason('');
      onChanged();
      return 'Düzeltme talebi açıldı. Not, bölüm sekreteri/öğrenci işleri onaylayana kadar değişmez.';
    });
  };

  return (
    <Card title="İlan edilen sonuçlar">
      <div className="alert info">Notlar ilan edildi ve giriş kilitli. Değişiklik için düzeltme talebi açın; <strong>ikinci bir kişinin onayı</strong> gerekir.</div>
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
      <Async state={results} isEmpty={(d) => d.items.length === 0} empty="Sonuç yok.">
        {(d) => (
          <Table>
            <thead><tr><th>Öğrenci no</th><th>Ad soyad</th><th>Not</th><th /></tr></thead>
            <tbody>
              {d.items.map((r) => {
                const st = byEnr.get(r.enrollment_id);
                return (
                  <tr key={r.result_id}>
                    <td>{st?.student_no ?? r.student_id}</td>
                    <td>{st?.full_name ?? '—'}</td>
                    <td><Badge tone={LETTER_TONE(r.letter)}>{r.letter}</Badge></td>
                    <td><button className="btn small" onClick={() => { setTarget(r); setLetter(LETTERS.find((l) => l !== r.letter) ?? 'BB'); }}>Düzeltme talep et</button></td>
                  </tr>
                );
              })}
            </tbody>
          </Table>
        )}
      </Async>
      {target && (
        <form className="form" onSubmit={submit} style={{ marginTop: 12 }}>
          <div className="form cols">
            <label className="field"><span>Yeni not</span><select value={letter} onChange={(e) => setLetter(e.target.value)}>{LETTERS.filter((l) => l !== target.letter).map((l) => <option key={l}>{l}</option>)}</select></label>
            <label className="field" style={{ gridColumn: 'span 2' }}><span>Gerekçe</span><input required value={reason} onChange={(e) => setReason(e.target.value)} /></label>
          </div>
          <div className="row"><button className="btn primary" disabled={act.busy}>Talebi gönder</button><button type="button" className="btn" onClick={() => setTarget(null)}>Vazgeç</button></div>
        </form>
      )}
    </Card>
  );
}

function ObjectionsPanel({ objections, onChanged }: { objections: ReturnType<typeof useFetch<Objection[]>>; onChanged: () => void }) {
  const [active, setActive] = useState<number | null>(null);
  const [accept, setAccept] = useState(true);
  const [letter, setLetter] = useState('BB');
  const [response, setResponse] = useState('');
  const act = useAction();

  const decide = (e: FormEvent) => {
    e.preventDefault();
    return act.run(async () => {
      await post(`/grade-objections/${active}/decision`, accept ? { accept: true, new_letter: letter, response } : { accept: false, response });
      setActive(null);
      setResponse('');
      onChanged();
      return accept ? 'İtiraz kabul edildi; düzeltme talebi ikinci onaya gönderildi.' : 'İtiraz reddedildi.';
    });
  };

  return (
    <Card title="Öğrenci itirazları">
      <Notice error={act.error} ok={act.ok} onClose={act.clear} />
      <Async state={objections} isEmpty={(d) => d.length === 0} empty="Bu şube için itiraz yok.">
        {(d) => (
          <Table>
            <thead><tr><th>Öğrenci no</th><th>Mevcut not</th><th>Gerekçe</th><th>Durum</th><th /></tr></thead>
            <tbody>
              {d.map((o) => (
                <tr key={o.id}>
                  <td>{o.student_no}</td>
                  <td>{o.letter}</td>
                  <td>{o.reason}</td>
                  <td><Badge tone={o.status === 'accepted' ? 'ok' : o.status === 'rejected' ? 'bad' : 'warn'}>{o.status === 'open' ? 'Açık' : o.status === 'accepted' ? 'Kabul' : 'Ret'}</Badge></td>
                  <td>{o.status === 'open' && <button className="btn small primary" onClick={() => { setActive(o.id); setAccept(true); }}>Karara bağla</button>}</td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Async>
      {active && (
        <form className="form" onSubmit={decide} style={{ marginTop: 12 }}>
          <div className="form cols">
            <label className="field"><span>Karar</span><select value={accept ? 'a' : 'r'} onChange={(e) => setAccept(e.target.value === 'a')}><option value="a">Kabul et (notu düzelt)</option><option value="r">Reddet</option></select></label>
            {accept && <label className="field"><span>Yeni not</span><select value={letter} onChange={(e) => setLetter(e.target.value)}>{LETTERS.map((l) => <option key={l}>{l}</option>)}</select></label>}
            <label className="field" style={{ gridColumn: 'span 2' }}><span>Öğrenciye yanıt</span><input required value={response} onChange={(e) => setResponse(e.target.value)} /></label>
          </div>
          <div className="row"><button className="btn primary" disabled={act.busy}>Kaydet</button><button type="button" className="btn" onClick={() => setActive(null)}>Vazgeç</button></div>
        </form>
      )}
    </Card>
  );
}

// T-1 kanıtı: kayıt açıldığı anda 5.000 öğrenci, kontenjanı 40 olan 30 şubeye istek atar.
// Her öğrenci 3 farklı şubeye kayıt isteği gönderir (toplam 15.000 istek) ve istekler VUS sanal kullanıcı
// üzerinden olabildiğince hızlı (kapalı döngü, bekleme yok) akıtılır: sunucu her an VUS eşzamanlı istek görür.
// (5.000 AYRI sanal kullanıcı, 7,6 GB'lık Docker belleğine sığmıyor: k6 VU başına ~5 MB kullanır.)
//
// Başarı ölçütü: sunucu hatası (5xx/zaman aşımı) yok; 201 (kayıt/bekleme), 409 ve 422 beklenen iş sonuçlarıdır.
// Kontenjan aşımı olmadığının asıl doğrulaması koşudan SONRA veritabanında yapılır ('simulator check').
import http from 'k6/http';
import exec from 'k6/execution';
import { check } from 'k6';
import { SharedArray } from 'k6/data';
import { Counter } from 'k6/metrics';

const data = JSON.parse(open('/data/tokens.json'));
const students = new SharedArray('students', () => data.students);
const sections = data.sections;

const STUDENTS = Math.min(Number(__ENV.STUDENTS || 5000), students.length);
const PER_STUDENT = 3;
const VUS = Number(__ENV.VUS || 500);
const BASE = __ENV.API_URL || 'http://backend:8080';

const enrolled = new Counter('result_enrolled');
const waitlisted = new Counter('result_waitlisted');

export const options = {
  scenarios: {
    registration_opens: {
      executor: 'shared-iterations',
      vus: VUS,
      iterations: STUDENTS * PER_STUDENT,
      maxDuration: '8m',
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],          // yalnızca 5xx/ağ hatası (beklenen durumlar aşağıda hariç)
    http_req_duration: ['p(95)<5000', 'p(99)<10000'],
    checks: ['rate>0.99'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

http.setResponseCallback(http.expectedStatuses(201, 409, 422));

export default function () {
  const i = exec.scenario.iterationInTest;
  const studentIdx = Math.floor(i / PER_STUDENT);
  const k = i % PER_STUDENT;
  const s = students[studentIdx];
  // 30 şubeden 3 farklı şube: 11, 30 ile aralarında asal olduğundan k=0,1,2 için farklı sonuç verir.
  const section = sections[((studentIdx * 7) + k * 11) % sections.length];

  const res = http.post(`${BASE}/sections/${section}/enrollments`, null, {
    headers: {
      Authorization: `Bearer ${s.token}`,
      'Idempotency-Key': `k6-${s.student_id}-${section}`,
    },
    tags: { name: 'POST /sections/{id}/enrollments' },
  });

  check(res, { 'sunucu hatası yok': (r) => r.status < 500 && r.status !== 0 });
  if (res.status === 201) {
    const body = res.json();
    if (body.status === 'enrolled') enrolled.add(1);
    if (body.status === 'waitlisted') waitlisted.add(1);
  }
}

export function handleSummary(data) {
  return {
    '/data/k6-summary.json': JSON.stringify(data, null, 2),
    stdout: textSummary(data),
  };
}

function textSummary(d) {
  const m = d.metrics;
  const t = m.http_req_duration.values;
  const line = (k, v) => `  ${k.padEnd(30)} ${v}\n`;
  let out = '\n=== k6 özeti: kayıt anı ===\n';
  out += line('eşzamanlı sanal kullanıcı', VUS);
  out += line('toplam istek', m.http_reqs.values.count);
  out += line('verim (istek/sn)', m.http_reqs.values.rate.toFixed(1));
  out += line('hatalı istek oranı (5xx)', (m.http_req_failed.values.rate * 100).toFixed(3) + ' %');
  out += line('gecikme ort', t.avg.toFixed(0) + ' ms');
  out += line('gecikme p(50)', t.med.toFixed(0) + ' ms');
  out += line('gecikme p(95)', t['p(95)'].toFixed(0) + ' ms');
  out += line('gecikme p(99)', t['p(99)'].toFixed(0) + ' ms');
  out += line('gecikme max', t.max.toFixed(0) + ' ms');
  out += line('kayıt (enrolled)', m.result_enrolled ? m.result_enrolled.values.count : 0);
  out += line('bekleme (waitlisted)', m.result_waitlisted ? m.result_waitlisted.values.count : 0);
  for (const [name, mt] of Object.entries(d.metrics)) {
    if (mt.thresholds) {
      for (const [k, v] of Object.entries(mt.thresholds)) out += `  eşik ${name} ${k}: ${v.ok ? 'GEÇTİ' : 'KALDI'}\n`;
    }
  }
  return out;
}

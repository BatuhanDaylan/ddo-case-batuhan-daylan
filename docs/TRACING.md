# Dağıtık izleme (OpenTelemetry)

Şartname: *"en az bir kritik akışta dağıtık izleme (OpenTelemetry trace)"*. İzlenen kritik akış: **ders kaydı**
(`POST /sections/{id}/enrollments`, görev T-1/T-2).

## Nasıl açılır

İzler varsayılan olarak **kapalıdır** (global noop sağlayıcı; maliyeti yok). Açmak için:

```bash
OTEL_TRACES_EXPORTER=stdout docker compose up -d backend
docker compose logs backend --no-log-prefix | grep '"TraceID"'
```

Gelen W3C `traceparent` başlığı kabul edilir (iz, çağıranın izine bağlanır); her yanıt iz kimliğini `X-Trace-Id`
başlığında döner. Span adı yol *desenidir* (`POST /sections/{id}/enrollments`), kimlik değil: iz sayısı kimliklerle çoğalmaz.

## Span hiyerarşisi

```
POST /sections/{id}/enrollments                  (HTTP sunucu span'i, middleware)
└─ registration.enroll                           (kök iş span'i; öznitelikler: student.id, section.id, enrollment.outcome)
   ├─ db.lock_student                            SELECT … FOR UPDATE (aynı öğrencinin eşzamanlı kayıtlarını sıraya sokar)
   ├─ rules.load_input                           kural girdisini yükler (mevcut şubeler, geçilen dersler, GANO, ön koşullar)
   ├─ rules.check                                saf kural denetimi (ön koşul, çakışma, tekrar, AKTS)
   ├─ db.lock_section                            şube satır kilidi + kontenjan kararı  ← kilit beklemesi burada görünür
   ├─ db.insert_enrollment                       kayıt satırı (+ bekleme sırası)
   └─ audit.write                                değiştirilemez denetim kaydı
```

Beklenen iş sonuçları (kural ihlali, zaten kayıtlı, kayıt kapalı) span'i **Error** yapmaz; `enrollment.outcome` özniteliği
`enrolled | waitlisted | rejected` olur. Yalnızca beklenmeyen hatalar (`error`) span durumunu bozar.
Bu davranış `internal/registration/trace_integration_test.go` ile sınanır.

## Gerçek sistemden yakalanan örnek (2026-10-30, yerel Docker)

Üç istek, aynı `traceparent` iz kimliğine (`4bf92f35…4736`) bağlı. Süreler span başlangıç/bitişinden:

**1) Başarılı kayıt** (`FIZ101`, şube 3): toplam **8,1 ms**

| Span | Süre | Not |
|---|---:|---|
| `POST /sections/{id}/enrollments` | 8,12 ms | HTTP |
| `registration.enroll` | 3,48 ms | kök iş span'i, `outcome=enrolled` |
| `db.lock_student` | 0,13 ms | |
| `rules.load_input` | 1,48 ms | **en pahalı aşama**: kural verisi (çoğunlukla okuma) |
| `rules.check` | 0,00 ms | saf bellek içi hesap |
| `db.lock_section` | 0,76 ms | kilit beklemesi yok (yük yok) |
| `db.insert_enrollment` | 0,57 ms | |
| `audit.write` | 0,47 ms | |

**2) Reddedilen kayıt** (`MAT101`: `BM101` ile ders saati çakışıyor): `rules.check` kuralı reddeder ve akış **orada durur**.
İzde `db.lock_section` **hiç yok**: reddedilecek isteğin şube kilidini hiç tutmadığı, yani kuralların kasıtlı olarak şube
kilidinden *önce* değerlendirilmesinin (DECISIONS.md #10) kritik bölümü kısa tuttuğu iz üzerinde görülür.

**3) Aynı `Idempotency-Key` ile tekrar:** yalnızca HTTP span'i oluşur (2,6 ms), `registration.enroll` **yoktur**: iş yeniden
çalışmadan kayıtlı yanıt döndü (idempotans).

## Yük altında nerede zaman harcanır?

Yük testinde (docs/LOAD_TEST.md) yüzlerce istek aynı şube için sıraya girer; bu bekleme `db.lock_section` span'inin
süresinde görünür. Tek istekte bu aşama 0,76 ms iken, aynı şubeye eşzamanlı yüklenen isteklerde süre kilit kuyruğu kadar
uzar. (Bu belgede yük altı iz sayıları **ölçülmedi**; izleme yük testinde kapalıydı, çünkü 15.000 isteğin izini standart
çıktıya yazmak ölçümü bozardı.)

## Sınırlamalar

- Dışa aktarım yalnızca `stdout`; OTLP/Jaeger/Tempo'ya gönderim eklenmedi (SDK değişikliği gerekmez, yalnızca farklı bir exporter).
- Yalnızca kayıt akışı alt span'lere ayrıldı; diğer uç noktalar yalnızca HTTP span'i üretir.
- Veritabanı sürücüsü otomatik izlenmiyor; DB aşamaları elle sarılmış span'lerdir.

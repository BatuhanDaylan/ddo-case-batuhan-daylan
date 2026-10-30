# Kaos senaryoları ve sonuçları

Şartname: *"en az 2 kaos senaryosu betiği (ör. veritabanı yeniden başlatma, ağ gecikmesi) ve sonuçları"*.
Üç senaryo yazıldı ve **gerçek sistemde çalıştırıldı**. Betikler `scripts/chaos/` altındadır; her biri sonunda
sistemin kendiliğinden toparlandığını **tutarlılık denetimi** (`simulator check`) ve **tam doğrulama** (`make verify`)
ile kanıtlar.

```bash
docker compose up -d --build
sh scripts/chaos/db-restart.sh        # A) yük altında veritabanı yeniden başlatma
sh scripts/chaos/network-latency.sh   # B) API ↔ veritabanı ağ gecikmesi (Toxiproxy)
sh scripts/chaos/db-freeze.sh         # C) veritabanı donması (docker pause)
```

Ortam: tek makine, Docker (20 CPU, 7,6 GB), tek API örneği, PostgreSQL 16. Veri: 12.000 öğrencilik simülasyon seti.

## Özet

| | Senaryo | Hipotez | Sonuç | Bulunan sorun |
|---|---|---|---|---|
| A | Yük altında DB yeniden başlatma | API çökmeden toparlanır, veri bozulmaz | ✅ geçti | **500 yerine 503 dönmeliydi** → düzeltildi |
| B | 150 ms ±50 ms ağ gecikmesi | Doğruluk korunur, hata yok | ✅ doğruluk geçti, ⚠️ verim 100× düştü | Ardışık gidiş-dönüş sayısı yüksek (iyileştirme fırsatı) |
| C | DB donması (docker pause) | İstekler askıda kalmaz, sonra toparlanır | ✅ geçti | **`/me` donmada 404 dönüyordu** → düzeltildi |

Üç senaryoda da: backend süreci **yeniden başlamadı**, kontenjan/sayaç/sıra tutarlılığı **PASS**, `make verify` **18/18 PASS**.

---

## A) Yük altında veritabanı yeniden başlatma

**Yöntem:** 3.000 öğrenci × 3 şube = 9.000 kayıt isteği, 60 eşzamanlı. Yük sürerken (3. sn) `docker compose restart db`.

**Sonuç (düzeltmeden sonra):**

| Ölçüm | Değer |
|---|---|
| Veritabanı yeniden başlatma komutu | 1,3 sn |
| API `/health`'in tekrar 200 olması (komut dönüşünden) | 0,6 sn |
| Toplam istek / süre | 9.000 / 5,4 sn (1.668 istek/sn) |
| Başarılı (`201`) | 7.545 |
| Kesinti sırasında | **1.455 × `503 service_unavailable`** (+ `Retry-After: 1`) |
| `500` (kod hatası) | **0** |
| Ağ hatası / askıda kalan istek | 0 (en uzun istek 1,18 sn) |
| Backend süreci yeniden başladı mı | **Hayır** (havuz kendiliğinden yeniden bağlandı) |
| Tutarlılık denetimi | **PASS** (kontenjan aşımı yok; sayaçlar, sıra, bekleme listesi tutarlı) |
| `make verify` | **18/18 PASS** |

**Bulgu ve düzeltme:** İlk koşuda kesinti sırasında **804 istek `500 internal`** döndü. Veritabanı geçici olarak ulaşılamaz
olduğunda 500 yanlıştır: "kodda hata var" demektir; istemciler ve yük dengeleyiciler 500'ü yeniden denemez. Doğrusu,
yeniden denenebilir olduğunu bildiren **`503` + `Retry-After`**. `httpx.Internal` artık geçici altyapı hatalarını
(bağlantı kopması, zaman aşımı, PostgreSQL `08xxx`/`57P0x` sınıfları) `503` olarak yanıtlar; gerçek hatalar `500` kalır
(`internal/httpx/transient_test.go`).

## B) API ↔ veritabanı arasında ağ gecikmesi

**Yöntem:** Veritabanının önüne **Toxiproxy** konuldu, ikinci bir API örneği (`chaos-api`) onun üzerinden bağlandı (normal
API'ye dokunulmadı). Aynı yük (300 öğrenci × 3 şube, 40 eşzamanlı, farklı öğrenci aralıkları) önce gecikmesiz, sonra
`latency=150 ms, jitter=50 ms` toxic'i ile koşuldu.

| | Gecikmesiz | 150 ms ±50 ms |
|---|---:|---:|
| Verim | 1.335 istek/sn | **13 istek/sn** |
| Gecikme p50 | 23 ms | 2,98 sn |
| Gecikme p95 | 57 ms | 4,33 sn |
| Gecikme p99 | 156 ms | 5,55 sn |
| `5xx` | 0 | **0** |
| Tüm istekler `201` | 900/900 | **900/900** |
| Tutarlılık denetimi | PASS | **PASS** |

**Yorum (dürüst):** Doğruluk gecikmeden etkilenmedi: tüm istekler tamamlandı, hiçbir istek hata vermedi, kontenjan
tutarlılığı bozulmadı. Ama verim **100 kat** düştü. Bu şaşırtıcı değil, **Little yasasıyla** açıklanır: 40 eşzamanlı istek ÷
~3 sn istek süresi ≈ 13 istek/sn. İstek süresi büyük, çünkü bir kayıt isteği yaklaşık **13 ardışık veritabanı gidiş-dönüşü**
yapar (öğrenci kilidi, kural verisinin ~10 okuması, şube kilidi, ekleme, denetim) ve her biri ağ gecikmesini öder.

**İyileştirme fırsatı (yapılmadı):** Bağımsız okumaları `pgx.Batch` ile tek gidiş-dönüşte göndermek ve şube kilidi altındaki
ekleme + sayaç + denetim yazımını tek toplu işleme indirmek, gecikmeli ağda verimi büyük ölçüde artırır. Yerel/aynı ağ
dağıtımında (gidiş-dönüş < 1 ms) bu kayda değer değildir; veritabanı farklı bir bölgedeyse kritik olur. Bu yüzden
**bilinen sınırlama** olarak kayıtlıdır.

## C) Veritabanı donması (`docker pause`)

Yeniden başlatma bağlantıyı hızla koparır; **donma** daha sinsidir: bağlantı açık kalır, hiçbir şey dönmez.

| Aşama | Gözlem | Beklenen | Sonuç |
|---|---|---|---|
| 1 · 8 sn donma | `GET /health` | askıda kalmadan ~2 sn'de 503 | ✅ **503, 2,0 sn** |
| 1 · 8 sn donma | donma sırasında başlayan `GET /me` | donma bitince tamamlanır | ✅ **200, 10,2 sn** (istek sağ çıktı) |
| 2 · 30 sn donma | `GET /me` | sonsuza dek askıda kalmaz | ✅ **503, 25,0 sn** (istek zaman aşımı) |
| her ikisi sonrası | `/health` tekrar 200 | kendiliğinden | ✅ **50–55 ms** sonra |
| | backend yeniden başladı mı | hayır | ✅ hayır |

**Bulgu ve düzeltme:** İlk koşuda donmuş veritabanında `GET /me` **`404 kullanıcı bulunamadı`** döndürdü: `Me` işleyicisi *her*
veritabanı hatasını "kayıt yok" sanıyordu. Veritabanı donması "kullanıcı yok" değildir. Şimdi yalnızca `pgx.ErrNoRows` 404'tür;
altyapı hatası `503` olur (`internal/auth/me_integration_test.go` bunu veritabanı kapatılarak sınar). Aynı kalıp başka bir
yerde aranıp bulunmadı.

**Not:** Donuk veritabanında istek süresi 25 sn ile sınırlıdır (istek zaman aşımı). Etkileşimli arayüz için bu uzundur;
sorgu başına daha kısa zaman aşımı (`statement_timeout`) eklenebilir ama yavaş uç noktaları (program çözücü) etkilememesi
için ayrıca düşünülmelidir.

---

## Betiklerin kendi hatası (dürüstlük notu)

`final_checks` ilk sürümünde `simulator check` çıktısını `grep PASS|FAIL` ile süzüyordu. Denetim çalışmayıp hiç çıktı üretmezse
**betik sessiz kalıp başarılıymış gibi görünüyordu**; senaryo C'nin ilk koşusunda bu gerçekten yaşandı (denetim çıktısı boştu).
Betik artık sonuç satırı yoksa açıkça `FAIL` yazıp ham çıktıyı basar. Kaos testinin yanlış güvence vermesi, hiç test
olmamasından kötüdür.

## Sınırlamalar

- Tek makine; gerçek ağ bölünmesi/paket kaybı denenmedi (Toxiproxy ile `reset_peer`, `timeout`, `bandwidth` toxic'leri denenebilir).
- Tek API örneği: örnekler arası yük dağılımı ve rolling restart denenmedi.
- Yalnızca PostgreSQL kesintileri denendi; disk dolması, bellek baskısı ve CPU kısıtı (`docker update --cpus`) denenmedi.
- Kayıt akışındaki yazma işlemleri tek transaction'dır; kesintide yarım kalan transaction'lar PostgreSQL tarafından geri alınır
  (tutarlılık denetimi bunu doğruladı), ancak istemci "isteğim işlendi mi?" belirsizliğinde kalabilir: `Idempotency-Key` ile
  aynı isteği güvenle yeniden göndermek bu yüzden zorunludur.

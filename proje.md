# UniNet — Üniversite Öğrenci Bilgi Sistemi (OBS)

> DDO Yazılım Ekibi · Alım Projesi
>
> Senaryo, kurum ve kişiler tamamen kurgusaldır; ancak sorunlar gerçek sistemlerde karşılaşılan türdendir.

## 1. Proje Amacı

Bir devlet üniversitesinin akademik yapısını, ders kayıt dönemlerini, not sürecini ve mezuniyet kontrolünü yöneten bir öğrenci bilgi sistemi geliştireceksin. Kayıt haftasında binlerce öğrenci aynı anda sisteme girer; kurallar karmaşık, hata toleransı sıfırdır.

Senden beklenen: bu problemi çözen, **Docker ile ayağa kalkan, frontend + backend + veritabanı** içeren, çalışan ve test edilmiş bir sistem geliştirmen.

## 2. Teknoloji Yığını

| Katman | Gereksinim |
|---|---|
| **Backend** | **Go** (1.22+). HTTP katmanı için standart kütüphane, chi, gin, echo, fiber vb. serbest. |
| **Veritabanı** | **PostgreSQL 16** (zorunlu). Sürücü olarak `pgx` önerilir; ORM, `sqlc` gibi araçlar serbesttir. |
| **Frontend** | **Serbest** (React, Vue, Svelte, Angular, HTMX …). Seçimini ve gerekçesini README’de yaz. |
| **Konteyner** | Docker + Docker Compose |

Yığın değişmez: backend Go ve veritabanı PostgreSQL olmak zorundadır.

## 3. Zorunlu Mimari ve Çalıştırma


Üç katman da zorunludur ve hepsi **tek komutla** çalışmalıdır:

```bash
git clone <senin-repo-adresin> && cd <repo>
cp .env.example .env
docker compose up --build
```

- **Frontend:** `http://localhost:3000`
- **Backend (Go API):** `http://localhost:8080`
- **Veritabanı:** PostgreSQL (host’a açılması gerekmez, yalnızca compose ağında erişilsin)
- **Veri üretici (simülatör):** ayrı bir compose servisi olarak (bkz. Bölüm 8). Go ile yazman önerilir.
- Servisler `healthcheck` içermeli; `depends_on` ile `service_healthy` koşulu kullanılmalı. Soğuk makinede (boş Docker önbelleği) 5 dakika içinde hazır olmalı.
- Migrasyon ve seed işlemleri compose ile **otomatik** çalışmalı; elle adım olmamalı. Örnek kullanıcı hesapları (her rol için bir tane) README’de listelenmeli.
- Backend `GET /health` ucu sağlık durumunu (veritabanı bağlantısı dahil) dönmeli.
- Konteynerlerde root olmayan kullanıcı kullan; imajları çok aşamalı (multi-stage) derle ve küçük tut.

## 4. Kullanıcı Rolleri

- Öğrenci
- Öğretim üyesi
- Danışman
- Bölüm sekreteri
- Öğrenci işleri
- Raporlama (rektörlük)

Her rolün yalnızca kendi görevine uygun ekranları ve uç noktaları görmesi, yetkisiz erişimin sunucu tarafında engellenmesi beklenir.

## 5. Kapsam (İşlevsel Gereksinimler)

- Akademik yapı: fakülte → bölüm → program → müfredat sürümleri
- Dönem ve şube yönetimi: kontenjan, derslik, saat, öğretim üyesi atama
- Ders kaydı: danışman onayı, ekle-sil haftası, bekleme listesi
- Haftalık ders programı ve sınav takvimi
- Not girişi: vize/final/ödev ağırlıkları, harf notu, bütünleme, itiraz
- Transkript ve belgeler (imzalı doğrulanabilir PDF)
- Mezuniyet kontrolü ve ‘kalan dersler’ raporu
- Raporlama: bölüm bazlı başarı, doluluk, GANO dağılımı

Bunlar asgari kapsamdır. Frontend gerçek veriyle çalışmalı; her ekranda yükleniyor / boş / hata durumları ele alınmalı, mobil genişlikte kullanılabilir olmalıdır.

## 6. Zorlu Çekirdek Görevler

Aşağıdaki 7 görev projenin asıl değerlendirilen kısmıdır. Yüzeysel bir uygulama yeterli değildir; her görev için çalıştığını **kanıtlayan** bir test, ölçüm ya da betik teslim et.

### Görev 1: Kayıt anı yarış durumu

Kayıt açıldığı saniyede 5.000 öğrenci kontenjanı 40 olan 30 şubeye istek atar. Kontenjan asla aşılmamalı, istekler adil sırayla (talep zamanı) işlenmeli, dolan şubede bekleme listesi oluşmalı ve boşalan yer sıradaki öğrenciye otomatik atanmalı. Kanıt: k6 yük testi + ardından veritabanında ‘kontenjan aşımı yok’ doğrulaması.

### Görev 2: Kural doğrulayıcı: ön koşul, çakışma, AKTS

Ön koşullar bir DAG’dır (müfredat kaydedilirken döngü reddedilmeli); ‘A ve (B veya C)’ gibi ifadeler desteklenmeli. Ders saati çakışması, aynı dersin tekrarı ve GANO’ya göre AKTS üst limiti (ör. 30/36/40) kontrol edilmeli. İhlaller tek transaction içinde değerlendirilip **hepsi birden** listelenmeli (ilk hatada durmamalı).

### Görev 3: Müfredat sürümleme ve denklik

Öğrenci hangi yıl girdiyse o yılın müfredatına tabi olmalı; müfredat değişse de eski öğrenciler etkilenmemeli. Denklik/muafiyet tablosu (ders X yerine Y sayılır) çalışmalı. Zaman yolculuğu sorgusu: ‘2021 girişli bir öğrencinin 2024 itibarıyla alması gereken dersler’ doğru dönmeli.

### Görev 4: Not hesaplama motoru

Mutlak ve bağıl (T-skoru, sınıf ortalaması + standart sapma) harf notu yöntemleri desteklenmeli. Bütünleme ve mazeret sınavı, not düzeltmesi için onay akışı ve tam geçmiş tutulmalı. Tekrar alınan derslerde ‘son not’ veya ‘en yüksek not’ politikasına göre GANO yeniden hesaplanmalı; bir not değiştiğinde etkilenen tüm öğrencilerin ortalaması idempotent bir arka plan job’ıyla güncellenmeli.

### Görev 5: Ders programı çözücü

Kısıtlar: öğretim üyesi müsaitliği, derslik kapasitesi ve tipi (lab/amfi), aynı yarıyılın zorunlu dersleri çakışmaz, öğretim üyesi aynı anda iki yerde olamaz. 60 ders / 12 derslik / 20 öğretim üyesi örnek verisiyle 30 saniye içinde çakışmasız bir program üretilmeli (backtracking, kısıt çözücü ya da yerel arama). Çözüm yoksa **hangi kısıtların** çatıştığı raporlanmalı.

### Görev 6: Mezuniyet denetim motoru

Kurallar kodda sabit değil, veri (JSON/DSL) olarak tanımlanmalı: zorunlu dersler, seçmeli havuzlar (ör. en az 3 ders ve 9 AKTS), staj, GANO ≥ 2.00. Bir ders birden fazla havuzda sayılabiliyorsa öğrenci için **en uygun atama** bulunmalı (eşleştirme / akış problemi). Çıktı ‘mezun olabilir’ ya da ‘şu dersler eksik’ olmalı.

### Görev 7: Yetki, denetim ve imzalı belge

Öğretim üyesi yalnızca kendi şubesinin notlarını, danışman yalnızca danışanlarını görmeli; not ilanından sonra giriş kilitlenmeli, geçmişe dönük değişiklikler çift onay istemeli. Her işlem değiştirilemez denetim kaydı bırakmalı. Transkript PDF’i HMAC imzalı bir QR içermeli ve `GET /verify/{kod}` ile doğrulanabilmeli.

## 7. Ortak Teknik Gereksinimler

Tüm projeler için geçerlidir:

- **Kimlik ve yetki:** parola özeti argon2id veya bcrypt ile; JWT (access + refresh) ya da sunucu taraflı oturum; rol tabanlı yetkilendirme (RBAC) ve **her** uç noktada yetki kontrolü. Yetkisiz erişim 401/403 ile reddedilmeli.
- **Veritabanı:** şema değişiklikleri migrasyon dosyalarıyla (goose, golang-migrate vb.) yapılmalı. Yabancı anahtar, `UNIQUE`, `CHECK` gibi kısıtlar tasarımın parçasıdır; para tutarları tam sayı (kuruş) ya da `numeric` olmalı, `float` yasak. Kritik sorgular için dizin tasarla ve `EXPLAIN (ANALYZE)` çıktısını README’ye ekle.
- **Eşzamanlılık:** bakiye, stok, kontenjan, rezervasyon gibi paylaşılan durumlar transaction + uygun kilit/izolasyon seviyesi (`SELECT … FOR UPDATE`, `SKIP LOCKED`, koşullu `UPDATE`, `SERIALIZABLE` + yeniden deneme vb.) ile korunmalı. Yarış durumu bir **testle kanıtlanmalı**.
- **İdempotans:** durum değiştiren kritik `POST` istekleri (ödeme, rezervasyon, sipariş vb.) `Idempotency-Key` başlığını desteklemeli; aynı anahtarla gelen tekrar istek aynı sonucu döndürmeli.
- **Denetim kaydı:** kritik işlemler (kim, ne zaman, neyi, eski/yeni değer) eklemeli-yalnız (append-only) bir denetim tablosuna yazılmalı.
- **API:** tutarlı hata biçimi (`{"error":{"code":"…","message":"…"}}`), sunucu tarafı girdi doğrulaması, büyük listelerde keyset sayfalama, anlamlı HTTP durum kodları.
- **Dayanıklılık:** `context` ile zaman aşımları, düzgün kapanış (graceful shutdown), yapılandırılmış JSON log (parola/token asla loglanmaz), tüm yapılandırma ortam değişkenlerinden.

## 8. Veri Üretici (Simülatör) ve Seed

Ayrı bir servis; 12.000 öğrenci, 600 ders, 1.500 şube, 4 yıllık geçmiş not verisi üretir; ‘kayıt anı’ modunda öğrencileri taklit eden eşzamanlı istekleri tetikler. Aynı tohumla aynı veri üretilmeli.

Veri üretici ayrı bir compose servisi olmalı ve seed edilebilir (aynı tohumla aynı veri) çalışmalıdır. Arayüzde sistemin canlı olduğu görülebilmelidir.

## 9. Kalite ve Teslim Kriterleri

- En az 60 otomatik test; çekirdek (domain) paketlerde kapsam ≥ %75; en az 2 adet fuzz veya özellik tabanlı (property-based) test.
- GitHub Actions ile build + test + lint + `govulncheck` + konteyner imajı derleme **zorunlu**.
- OpenAPI dokümantasyonu, Prometheus `/metrics` ve en az bir kritik akışta dağıtık izleme (OpenTelemetry trace).
- k6 yük testi ve hedef gecikme/başarı değerlerini (SLO) gösteren tablo; hedeflerin tutup tutmadığı dürüstçe raporlanmalı.
- `DECISIONS.md` içinde en az 7 mimari karar kaydı.
- En az 2 kaos senaryosu betiği (ör. veritabanı yeniden başlatma, ağ gecikmesi) ve sonuçları.
- `RUNBOOK.md`: yedekleme / geri yükleme adımları ve bir **sıfır kesintili migrasyon** örneği (expand → migrate → contract).

## 10. Hızlı Doğrulama Yapısı (Zorunlu)

Ekip yöneticisi projeyi teslimden sonra kısa sürede kontrol edecek. Bu yüzden tüm özelliklerin **tek komutla** denenebilmesi gerekir.

**Zorunlu çıktılar**

1. **Otomatik doğrulama komutu:** repo kökünde `make verify` (ya da `docker compose --profile verify run --rm verify`) çalıştırıldığında, ayağa kalkmış ve seed edilmiş sistem üzerinde aşağıdaki listedeki **her özellik ve her zorlu görev** API (ve mümkünse arayüz) üzerinden uçtan uca denenmeli. Komut sonunda her kimlik için `PASS` / `FAIL` yazan bir tablo basmalı ve hata varsa sıfırdan farklı çıkış kodu dönmeli. Süre 15 dakikayı aşmamalı (uzun yük testleri ayrı `make load` komutunda olsun).
2. **`TESTING.md`:** (a) her rol için demo hesabı (e-posta / parola), (b) aşağıdaki kimliklerin her biri için “nasıl denenir / beklenen sonuç” tablosu, (c) arayüzde elle denenecek kısa senaryo listesi.
3. **Elle deneme koleksiyonu:** tüm uç noktaları içeren bir `requests.http` (VS Code REST Client) ya da Postman koleksiyonu, demo hesaplarla çalışır durumda.
4. **Sıfırlama:** `make reset-demo` verileri ilk seed haline döndürmeli; böylece doğrulama tekrarlanabilir olmalı.
5. **Kanıt komutları:** her zorlu görev için kanıtı üreten komut (test, betik ya da ölçüm) `TESTING.md` içinde görev kimliğiyle listelenmeli.

**Doğrulama kimlikleri**

Kapsam maddeleri:

- `F-1` Akademik yapı: fakülte → bölüm → program → müfredat sürümleri
- `F-2` Dönem ve şube yönetimi: kontenjan, derslik, saat, öğretim üyesi atama
- `F-3` Ders kaydı: danışman onayı, ekle-sil haftası, bekleme listesi
- `F-4` Haftalık ders programı ve sınav takvimi
- `F-5` Not girişi: vize/final/ödev ağırlıkları, harf notu, bütünleme, itiraz
- `F-6` Transkript ve belgeler (imzalı doğrulanabilir PDF)
- `F-7` Mezuniyet kontrolü ve ‘kalan dersler’ raporu
- `F-8` Raporlama: bölüm bazlı başarı, doluluk, GANO dağılımı

Zorlu görevler:

- `T-1` Kayıt anı yarış durumu
- `T-2` Kural doğrulayıcı: ön koşul, çakışma, AKTS
- `T-3` Müfredat sürümleme ve denklik
- `T-4` Not hesaplama motoru
- `T-5` Ders programı çözücü
- `T-6` Mezuniyet denetim motoru
- `T-7` Yetki, denetim ve imzalı belge

Ek olarak: `GET /health`, kimlik doğrulama (yanlış parola, yetkisiz rol) ve yetki ihlali senaryoları da `make verify` içinde bulunmalıdır.

## 11. Bonus (Zorunlu Değil)

- Danışman için ‘riskli öğrenci’ uyarısı (GANO düşüşü, devamsızlık)
- Ders programını sürükle-bırak düzenleyen arayüz, çakışmayı anında gösterir
- Yoğunluk grafikleri (kayıt anındaki istek/sn)

## 12. README’de Cevaplanacak Doğrulama Soruları

README’ne `## Doğrulama Soruları` başlığı aç ve şunları cevapla:

1. En zorlandığın görev hangisiydi? Hangi alternatif çözümleri düşündün ve neden eledin?
2. Çözümünün kırıldığı ya da yetersiz kaldığı bir senaryo bul ve nedenini açıkla (bilinen sınırlama).
3. Veritabanına eklediğin dizinlerden ikisini seç: hangi sorgu için, dizinsiz/dizinli ölçüm farkı nedir?
4. Yapay zekâ kullandıysan, reddettiğin ya da düzelttiğin bir çıktıyı (önce/sonra) göster ve neden yanlış olduğunu açıkla.
5. “Kayıt anı yarış durumu” görevindeki çözümünün doğru çalıştığını nasıl kanıtladın? Komutu ve çıktısını yaz.

## 13. Teslim: GitHub (Public) ve Anket

1. Projeyi **kendi GitHub profilinde PUBLIC** bir depo olarak yayınla (öneri depo adı: `ddo-case-batuhan-daylan`). Private ya da erişilemeyen depolar değerlendirmeye alınmaz.
2. Commit geçmişi anlamlı olmalı: tek commit ile yüklenmiş proje kabul edilmez. Commit mesajları ne yaptığını anlatmalı.
3. README’nin başında kendi adını, kurulum adımlarını, mimari özetini ve karar gerekçelerini yaz.
4. Depoyu yayınladıktan sonra **depo bağlantısını şu ankete yükle:** https://banukampus.bandirma.edu.tr/a/s/ZB76YDBA
5. **Son teslim: 26 Ekim 2026, saat 23.59.** Depo bağlantısını bu saate kadar ankete yüklemiş olmalısın.
6. Teslimden sonra depoyu private yapma, silme ya da yeniden yazma (force push).

## 14. Kurallar ve Yapay Zekâ Kullanımı

- Yapay zekâ araçlarını (Claude, ChatGPT, Copilot, Cursor vb.) istediğin gibi kullanabilirsin; kodu, SQL’i, testleri ve dokümanı onlarla üretmen serbesttir. Önemli olan sonucun çalışması, kanıtlanması ve kararlarının açıklanabilmesidir.
- Teslimden sonra ekiple yaklaşık 20 dakikalık bir değerlendirme görüşmesi yapılacak: sistemi çalıştırıp göstermen, birkaç tasarım kararını açıklaman ve README’deki sorulara cevap vermen istenebilir.
- Başkasının çözümünü kopyalamak ya da paylaşmak elemeyi gerektirir. Her adaya farklı bir proje verilmiştir.
- **Son teslim: 26 Ekim 2026, saat 23.59.** Bu saatten sonra yüklenen bağlantılar ya da sonradan yapılan commit’ler değerlendirmeye alınmaz.

## 15. Değerlendirme Ölçütleri

| Ölçüt | Puan |
|---|---|
| Docker ile tek komutla çalışma, sağlık kontrolleri, seed | 10 |
| Zorlu çekirdek görevlerin doğruluğu ve derinliği | 40 |
| Mimari ve kod kalitesi | 15 |
| Test, CI ve hızlı doğrulama yapısı (`make verify`, `TESTING.md`) | 15 |
| Dokümantasyon ve gerekçeler (README, karar kayıtları) | 10 |
| Git geçmişi ve teslim hijyeni | 5 |
| Değerlendirme görüşmesi (sistemi gösterme, kararları açıklama) | 5 |
| Ekip yöneticisi incelemesi | 50 |
| **Toplam** | **150** |

**Ekip yöneticisi incelemesi (50 puan):** DDO ekip yöneticisi teslim edilen projeyi bütünüyle inceler: sistemin çalışırken verdiği genel izlenim ve kullanılabilirlik, kodun okunabilirliği ve bakımının kolaylığı, alınan teknik kararların mantığı ve README’deki anlatımın açıklığı, işi sahiplenme ve iletişim. Bu bölüm yöneticinin takdirine dayanır; diğer ölçütlerden bağımsız olarak puanlanır.

Kısmi çözümler değerlidir: bitirilmemiş bir zor görevi dürüstçe “yapılmadı, nedeni şu” diye belirtmek, çalışmayan bir çözümü çalışıyormuş gibi sunmaktan iyidir.


---
Başarılar dileriz — DDO Yazılım Ekibi

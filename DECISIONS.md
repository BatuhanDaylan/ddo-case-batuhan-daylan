# Mimari Karar Kayıtları

Her kayıt: bağlam, karar, neden, bedeli.

## 1. Kontenjan koruması: şube satır kilidi (`SELECT … FOR UPDATE`) + CHECK kısıtı
**Bağlam:** Kayıt anında binlerce istek aynı şubeye gelir; kontenjan aşılmamalı, bekleme sırası adil olmalı.
**Karar:** Her kayıt/bırakma işlemi önce şube satırını kilitler. `sections.enrolled_count` için ayrıca
`CHECK (enrolled_count <= capacity)` vardır.
**Neden:** Kilit "kontrol et → artır" arasına girişi engeller ve `enrollments.id`'lerin kilit alma
sırasıyla verilmesini sağlar (bekleme listesi = id sırası). CHECK ise uygulama hatasına karşı ikinci savunma
hattıdır. Alternatif olan koşullu `UPDATE … WHERE enrolled_count < capacity` kontenjanı korur ama bekleme
sırasının adaletini garanti etmez; `SERIALIZABLE` ise yoğun çakışmada çok sayıda yeniden deneme üretir.
**Kanıt:** `internal/registration/service_integration_test.go` (500 eşzamanlı istek). Kilit kaldırılınca test
kırılır (mutasyon kontrolü yapıldı).
**Bedeli:** Aynı şubeye gelen işlemler seri işlenir. Şube başına yüzlerce istek ve işlem başına birkaç ms
olduğundan kabul edilebilir; farklı şubeler birbirini beklemez.

## 2. Boşalan koltuğun bekleme listesine atanması aynı transaction'da
**Karar:** `DropTx`, bırakma ile bekleme listesinin ilk kişisinin terfisini tek transaction'da yapar; koltuk
el değiştirirken `enrolled_count` değişmez.
**Neden:** Arada "boş ama kimseye atanmamış" bir an olmaz; arka plan job'ı ve ikinci bir kilit gerekmez.

## 3. İdempotans anahtarı iş mantığıyla aynı transaction'da
**Karar:** `idempotency.Do`, anahtar satırını işle aynı transaction'da yazar. Eşzamanlı aynı anahtar,
`INSERT … ON CONFLICT` üzerinde ilk işlem bitene kadar bekler ve kaydedilen yanıtı aynen döner.
**Neden:** "Anahtar yazıldı ama iş yapılmadı" veya "iş yapıldı ama anahtar yazılmadı" durumları oluşamaz.
Aynı anahtarın farklı istekte kullanımı 422 ile reddedilir (fingerprint).

## 4. Denetim kaydı değiştirilemezliği veritabanında
**Karar:** `audit_log` üzerinde `UPDATE/DELETE/TRUNCATE`'i reddeden tetikleyiciler vardır.
**Neden:** Uygulama koduna güvenmek yerine, yetkili bir bağlantı bile (yanlışlıkla) kaydı değiştiremez.
**Bedeli:** Test verisi temizlenemez; testler geçici veritabanı kullanır.

## 5. Kimlik doğrulama: argon2id + kısa ömürlü JWT + rotasyonlu refresh token
**Karar:** Access token HS256, 15 dk; refresh token 256 bit rastgele, veritabanında yalnızca SHA-256 özeti,
her kullanımda döner. İptal edilmiş refresh token tekrar gelirse kullanıcının tüm oturumları kapanır.
**Neden:** Çalınan refresh token'ın yeniden kullanımı tespit edilir; veritabanı sızsa bile token'lar kullanılamaz.
Yalnızca HS256 kabul edilir (`alg=none` ve algoritma karıştırma saldırıları testle kapatıldı).

## 6. Müfredat sürümleri değişmezdir; öğrenci giriş yılına göre çözülür
**Karar:** Müfredat değişikliği yeni bir `curriculum_versions` satırıdır. Öğrencinin müfredatı =
programın `effective_year <= entry_year` olan en yeni sürümü.
**Neden:** Sonradan eklenen sürümler eski öğrencilerin sonucunu değiştiremez.

## 7. Keyset sayfalama
**Karar:** Listeler `?after=<son id>&limit=<n>` ile sayfalanır (`OFFSET` yok).
**Neden:** Sayfa derinliğinden bağımsız sabit maliyet; araya kayıt girse bile kayma/tekrar olmaz.

## 8. Kural doğrulayıcı saf pakette (`internal/rules`), veritabanından bağımsız
**Karar:** Ön koşul ifadeleri, döngü tespiti ve kayıt kuralları (çakışma, tekrar, AKTS) veritabanına dokunmayan
saf fonksiyonlardır. Girdiyi `registration` paketi yükler, `rules.Check` değerlendirir.
**Neden:** Kural mantığı DB'siz, milisaniyede ve yoğun testlenebilir (kapsam %99, özellik tabanlı döngü testi,
fuzz). `Check` ilk hatada durmaz; bütün ihlalleri tek raporda döner.

## 9. Ön koşul ifadesi JSON ağacı; döngü denetimi advisory kilit altında
**Karar:** `A ve (B veya C)` gibi ifadeler `{"op":"and","args":[…]}` ağacı olarak `course_prerequisites.expr`
(JSONB) içinde saklanır. Kaydederken derinlik/düğüm sınırı uygulanır, DFS ile döngü aranır; tüm grafik
değişikliği `pg_advisory_xact_lock` altında yapılır.
**Neden:** İki yönetici aynı anda A→B ve B→A girerse kilitsiz durumda ikisi de "döngü yok" görürdü.
**Bedeli:** Grafik değişiklikleri seri işlenir; ön koşul tanımı seyrek olduğundan önemsizdir.

## 10. Kural denetimi şube kilidinden önce; öğrenci satırı kilitlenir
**Karar:** `EnrollTx` sırası: öğrenci kilidi → kural denetimi → şube kilidi. `DropTx` öğrenci kilidi almaz.
**Neden:** Ağır okumalar T-1'deki kritik bölümü (şube kilidi) uzatmaz. Öğrenci kilidi, aynı öğrencinin iki
şubeye eşzamanlı kaydolup AKTS/çakışma kuralını aşmasını önler (testle kanıtlandı: 10 eşzamanlı istek, limit 30
AKTS → tam 5 başarılı). Kilit sırası tek yönlü olduğundan kilitlenme (deadlock) oluşmaz.

## 11. Müfredat öğrencinin GİRİŞ yılına bağlanır; "as_of" yalnızca vadeyi belirler
**Karar:** `GET /students/{id}/required-courses?as_of=YYYY`, müfredat sürümünü `entry_year`'a göre çözer;
`as_of` yalnızca öğrencinin o tarihte kaçıncı öğretim yılında olduğunu (`StudyYear`) ve derslerin vadesini
(`overdue/current/future`) belirler.
**Neden:** "2021 girişli öğrencinin 2024 itibarıyla alması gereken dersler" sorusu, 2024'te açılan yeni
müfredattan etkilenmemeli. Mantık veritabanından bağımsız `rules` paketindedir ve birim testlidir.

## 12. Denklik tek seviyelidir, muafiyet notsuz ve GANO dışıdır
**Karar:** `course_equivalences` "X yerine Y sayılır" kaydıdır; zincirleme (A≡B, B≡C ⇒ A≡C) uygulanmaz.
Muafiyet `course_results.exempt = true` satırıdır (harf `MU`), GANO sorgusu bunları dışlar; yalnızca
öğrenci işleri verebilir.
**Neden:** Zincirleme denklik, yönetici hatasıyla beklenmedik dersleri "geçilmiş" saydırabilir; açık kayıt
girmek denetlenebilirliği artırır. Denklik hem ön koşul/tekrar kontrolünde hem müfredat durumunda aynı
`ExpandPassed` fonksiyonuyla uygulanır, böylece iki yer birbirinden ayrışmaz.

## 13. Not hesabı saf pakette; bütünleme/mazeret ayrı puan olarak saklanır
**Karar:** `internal/grading` ağırlıklı toplam, mutlak/bağıl (T-skoru) harf notu ve GANO'yu veritabanından
bağımsız hesaplar. Her bileşen için normal, bütünleme ve mazeret puanı ayrı satırdır; geçerli olan
bütünleme > mazeret > normal önceliğiyle seçilir.
**Neden:** Hiçbir puan silinmez/ezilmez (denetlenebilir), hesap kuralı tek yerdedir ve özellik testleriyle
(harf tekdüzeliği: yüksek puan daha kötü not almaz; best ≥ last) güvence altındadır.
Bağıl yöntemde 40 puanın altı doğrudan FF'dir (düşük sınıf ortalaması kimseyi hak etmediği notla geçirmesin).
**Bilinen sınırlama:** Bütünleme/mazeret puanları ilandan ÖNCE girilir; ilandan sonraki bir değişiklik
düzeltme talebi (çift onay) yoluyla yapılır.

## 14. Not düzeltmesi çift onaylı, geçmiş değiştirilemez
**Karar:** Öğretim üyesi düzeltme talep eder; bölüm sekreteri/öğrenci işleri karar verir. Talep eden ile
onaylayan farklı olmalıdır (rol + `CHECK (decided_by <> requested_by)`). Her uygulanan değişiklik
`grade_history`'ye eski/yeni notla yazılır; tablo `audit_log` ile aynı tetikleyicilerle değiştirilemezdir.
Aynı sonuç için aynı anda yalnızca bir bekleyen talep olabilir (kısmi UNIQUE dizin).
**Neden:** Geçmişe dönük not değişikliği tek kişinin kararıyla yapılamaz; onay anında sonuç talep anındaki
notla eşleşmiyorsa (`stale_request`) onay reddedilir.

## 15. GANO önbelleği + idempotent kuyruk işçisi
**Karar:** `students.gano` bir önbellektir; kaynak `course_results`'tır. Not değişince etkilenen öğrenciler
`gano_jobs`'a yazılır; işçi (`FOR UPDATE SKIP LOCKED`) her seferinde sıfırdan hesaplayıp yazar.
Kuyruğa yazarken `ON CONFLICT DO UPDATE` kullanılır.
**Neden:** İşçi bir satırı işlerken aynı öğrenci için yeni değişiklik gelirse `DO NOTHING` bu değişikliği
kaybettirirdi; `DO UPDATE` satırı işçi bitince yeniden oluşturur. İş tekrar çalışsa aynı değeri yazar (testli).
Tekrar-alma politikası (`last`/`best`) program başına seçilir; değişince programdaki tüm öğrenciler kuyruğa girer.
Kayıt doğrulayıcının AKTS limiti de GANO'yu aynı `LoadGANO` fonksiyonundan alır, böylece tanım tektir.

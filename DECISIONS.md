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

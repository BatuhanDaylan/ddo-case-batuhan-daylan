# Yük testi: kayıt anı (Görev T-1)

Şartname: *"Kayıt açıldığı saniyede 5.000 öğrenci kontenjanı 40 olan 30 şubeye istek atar. Kontenjan asla aşılmamalı,
istekler adil sırayla işlenmeli, dolan şubede bekleme listesi oluşmalı. Kanıt: k6 yük testi + ardından veritabanında
'kontenjan aşımı yok' doğrulaması."*

## Nasıl çalıştırılır

```bash
make load      # seed (12.000 öğrenci) → token üret → k6 → veritabanı doğrulaması
```

`make load` sırayla şunları yapar (elle de çalıştırılabilir):

```bash
docker compose --profile sim  run --rm simulator seed                                 # 12.000 öğrenci, 600 ders, 1.500 şube, 4 yıllık not
docker compose --profile sim  run --rm simulator tokens -n 5000 -out /data/tokens.json # 5.000 öğrenci için token
docker compose --profile load run --rm k6                                              # yük
docker compose --profile sim  run --rm simulator check                                # "kontenjan aşımı yok" doğrulaması
```

## Senaryo

- 5.000 öğrenci × 3 farklı şubeye kayıt isteği = **15.000 istek**; hedef: kontenjanı 40 olan 30 şube (toplam 1.200 koltuk).
- Her istek benzersiz `Idempotency-Key` taşır; istekler tam karışık sırada gelir.
- Kapalı döngü: 500 sanal kullanıcı, bekleme süresi yok → sunucu her an **500 eşzamanlı istek** görür.
- Her kayıt isteği tam kural denetiminden (ön koşul, çakışma, AKTS) ve öğrenci/şube satır kilitlerinden geçer.

## Sonuçlar (2026-10-30, yerel Docker: 20 CPU, 7,6 GB, tek API örneği, PostgreSQL 16)

| Ölçüm | Sonuç |
|---|---|
| Toplam istek | 15.000 |
| Süre | 10,1 sn (k6 toplamı 15 sn) |
| Verim | **1.482 istek/sn** |
| Sunucu hatası (5xx / ağ) | **%0,000** |
| Gecikme ortalama / p50 | 329 ms / 316 ms |
| Gecikme p95 / p99 / maks | **426 ms** / **495 ms** / 580 ms |
| Kayıt alan (`enrolled`) | **1.200** (= 30 × 40, tam doluluk) |
| Bekleme listesine giren (`waitlisted`) | **13.800** |

### Veritabanı doğrulaması (`simulator check`) → **PASS**

| Denetim | İhlal |
|---|---|
| `enrolled_count > capacity` olan şube | 0 |
| Gerçek `enrolled` satır sayısı kontenjanı aşan şube | 0 |
| `enrolled_count` ile gerçek satır sayısı tutarsız şube | 0 |
| Aynı öğrencinin aynı şubede birden çok aktif kaydı | 0 |
| Sıra ihlali (sonradan gelen koltuk almış, önce gelen bekliyor) | 0 |
| Boş koltuk varken bekleyen | 0 |

## SLO tablosu: hedefler tuttu mu?

| SLO | Hedef | Ölçülen | Durum |
|---|---|---|---|
| Kontenjan aşımı | 0 | 0 | ✅ tuttu |
| Sunucu hatası oranı | < %1 | %0 | ✅ tuttu |
| Gecikme p95 | < 5.000 ms | 426 ms | ✅ tuttu |
| Gecikme p99 | < 10.000 ms | 495 ms | ✅ tuttu |
| Bekleme listesi sırası adil | ihlal yok | 0 | ✅ tuttu |

## Dürüst notlar ve sınırlamalar

- **Eşzamanlılık 500, 5.000 değil.** k6 her sanal kullanıcı için ~5 MB bellek kullanır; 5.000 *ayrı* sanal kullanıcı
  7,6 GB'lık Docker belleğine sığmadı (1.280. kullanıcıda süreç bellek yetersizliğinden öldürüldü, çıkış kodu 137).
  Bu yüzden aynı toplam yük (5.000 öğrenci, 15.000 istek) 500 sanal kullanıcı üzerinden akıtıldı. Sunucu açısından
  fark, "aynı anda bağlı istemci" sayısıdır (500 vs 5.000); kontenjan doğruluğu bundan etkilenmez, çünkü korunma
  veritabanı satır kilidiyle sağlanır. 5.000 eşzamanlı bağlantıda gecikmenin nasıl değişeceği **ölçülmedi**.
- **Tek makine.** İstemci, API ve PostgreSQL aynı makinede; gerçek ağ gecikmesi yok.
- **Tek API örneği.** Yatay ölçekleme denenmedi. Doğruluk veritabanı kilidine dayandığı için çoklu örnekte de
  korunmalıdır, ama bu bir ölçümle doğrulanmadı.
- **Veritabanı havuzu** 40 bağlantı (`DB_MAX_CONNS`). Daha düşük değerlerde istekler havuzda kuyruğa girer.
- **Argon2 sınırı.** Giriş doğrulaması en fazla 4 eşzamanlı çalışır (`HASH_CONCURRENCY`); her biri ~64 MiB kullanır.
  Bu yük testi giriş yapmaz (token'lar doğrudan imzalanır); eşzamanlı giriş yükü ayrıca ölçülmedi.

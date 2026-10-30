-- +goose Up
-- EXPLAIN (ANALYZE) ile ÖLÇÜLEREK gereksiz bulunan iki dizin (ölçümler: docs/EXPLAIN.md, sorgu 2 ve 3).
--
-- course_results_student_idx (student_id):
--   Aynı tabloda UNIQUE (student_id, course_id, term_id) kısıtının dizini zaten student_id ile başlar ve
--   `WHERE student_id = ?` sorgularına hizmet eder. Düşürülünce plan bu dizine geçti; süre 0,223 ms → 0,207 ms (fark yok).
--   Yinelenen dizin = her not eklemesinde fazladan yazma maliyeti + 2,2 MB.
--
-- course_results_term_idx (term_id, course_id):
--   Dönem bazlı rapor sorgusu bir dönemin tablonun ~%20'sini oluşturduğu veride seçici değildir. Dizinle plan bitmap taramaya
--   gitti ve 30,8 ms sürdü; dizinsiz paralel sıralı tarama 18,9 ms sürdü (dizin daha YAVAŞ). Veri/dönem sayısı büyüyüp dönem
--   başına düşen oran küçülürse (örn. <%5) yeniden eklenmelidir; o zaman ölçümle gerekçelendirilmelidir.
DROP INDEX IF EXISTS course_results_student_idx;
DROP INDEX IF EXISTS course_results_term_idx;

-- +goose Down
CREATE INDEX course_results_student_idx ON course_results (student_id);
CREATE INDEX course_results_term_idx ON course_results (term_id, course_id);

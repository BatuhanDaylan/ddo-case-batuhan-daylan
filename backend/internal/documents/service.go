package documents

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/batuhan-daylan/uninet/internal/audit"
	"github.com/batuhan-daylan/uninet/internal/gradebook"
)

type Service struct {
	pool    *pgxpool.Pool
	signer  *Signer
	baseURL string
}

func NewService(pool *pgxpool.Pool, signer *Signer, publicBaseURL string) *Service {
	return &Service{pool: pool, signer: signer, baseURL: publicBaseURL}
}

type Issued struct {
	ID        int64     `json:"id"`
	Code      string    `json:"code"`
	VerifyURL string    `json:"verify_url"`
	PDFURL    string    `json:"pdf_url"`
	IssuedAt  time.Time `json:"issued_at"`
}

func (s *Service) verifyURL(code string) string { return s.baseURL + "/verify/" + code }

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// BuildSnapshot öğrencinin o andaki transkript içeriğini toplar.
func BuildSnapshot(ctx context.Context, q queryer, studentID int64, now time.Time) (Snapshot, error) {
	snap := Snapshot{Kind: "transcript", IssuedAt: now.UTC().Truncate(time.Second), Results: []ResultRow{}}
	err := q.QueryRow(ctx,
		`SELECT s.student_no, u.full_name, p.name, d.name, f.name, s.entry_year
		   FROM students s JOIN users u ON u.id = s.user_id JOIN programs p ON p.id = s.program_id
		   JOIN departments d ON d.id = p.department_id JOIN faculties f ON f.id = d.faculty_id
		  WHERE s.id = $1`, studentID).
		Scan(&snap.StudentNo, &snap.FullName, &snap.Program, &snap.Department, &snap.Faculty, &snap.EntryYear)
	if err != nil {
		return snap, err
	}
	rows, err := q.Query(ctx,
		`SELECT t.code, c.code, c.name, c.ects, cr.letter, cr.grade_point::float8, cr.passed, cr.exempt
		   FROM course_results cr JOIN courses c ON c.id = cr.course_id JOIN terms t ON t.id = cr.term_id
		  WHERE cr.student_id = $1 ORDER BY cr.term_id, c.code`, studentID)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	passedCourses := map[string]bool{}
	for rows.Next() {
		var r ResultRow
		if err := rows.Scan(&r.Term, &r.Code, &r.Name, &r.ECTS, &r.Letter, &r.Point, &r.Passed, &r.Exempt); err != nil {
			return snap, err
		}
		snap.Results = append(snap.Results, r)
		if r.Passed && !passedCourses[r.Code] {
			passedCourses[r.Code] = true
			snap.PassedECTS += r.ECTS
		}
	}
	if err := rows.Err(); err != nil {
		return snap, err
	}
	rows.Close()
	snap.GANO, err = gradebook.LoadGANO(ctx, q, studentID)
	return snap, err
}

// Issue belgeyi kendi transaction'ında düzenler.
func (s *Service) Issue(ctx context.Context, studentID, issuedBy int64, ip string) (Issued, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Issued{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := s.IssueTx(ctx, tx, studentID, issuedBy, ip)
	if err != nil {
		return Issued{}, err
	}
	return out, tx.Commit(ctx)
}

// IssueTx belgeyi çağıranın transaction'ında düzenler: içeriği dondurur, özetini ve kimliğini HMAC ile
// imzalar, denetime yazar. Commit çağıranındır (Idempotency-Key anahtarıyla aynı transaction'da yazılabilsin diye).
func (s *Service) IssueTx(ctx context.Context, tx pgx.Tx, studentID, issuedBy int64, ip string) (Issued, error) {
	snap, err := BuildSnapshot(ctx, tx, studentID, time.Now())
	if err != nil {
		return Issued{}, err
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return Issued{}, err
	}
	sha := SHA256Hex(raw)

	var out Issued
	if err := tx.QueryRow(ctx,
		`INSERT INTO documents (kind, student_id, snapshot, snapshot_sha256, issued_by, issued_at)
		 VALUES ('transcript', $1, $2, $3, $4, $5) RETURNING id`,
		studentID, string(raw), sha, issuedBy, snap.IssuedAt).Scan(&out.ID); err != nil {
		return Issued{}, err
	}
	out.Code = s.signer.Code(out.ID, sha)
	out.VerifyURL = s.verifyURL(out.Code)
	out.PDFURL = "/documents/" + strconv.FormatInt(out.ID, 10) + "/pdf"
	out.IssuedAt = snap.IssuedAt

	if err := audit.Write(ctx, tx, audit.Entry{
		ActorID: &issuedBy, Action: "document.issued", Entity: "document", EntityID: strconv.FormatInt(out.ID, 10),
		New: map[string]any{"kind": "transcript", "student_id": studentID, "sha256": sha}, IP: ip,
	}); err != nil {
		return Issued{}, err
	}
	return out, nil
}

var ErrDocumentNotFound = errors.New("belge bulunamadı")

type stored struct {
	StudentID int64
	Raw       string
	SHA       string
}

func (s *Service) load(ctx context.Context, id int64) (stored, error) {
	var d stored
	err := s.pool.QueryRow(ctx, `SELECT student_id, snapshot, snapshot_sha256 FROM documents WHERE id = $1`, id).
		Scan(&d.StudentID, &d.Raw, &d.SHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrDocumentNotFound
	}
	return d, err
}

// StudentOf belgenin sahibi öğrencinin kimliğini verir (yetki denetimi için).
func (s *Service) StudentOf(ctx context.Context, docID int64) (int64, error) {
	d, err := s.load(ctx, docID)
	return d.StudentID, err
}

// PDF saklanan içerikten PDF'i yeniden üretir (kodun aynısı QR'a basılır).
func (s *Service) PDF(ctx context.Context, docID int64) ([]byte, error) {
	d, err := s.load(ctx, docID)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(d.Raw), &snap); err != nil {
		return nil, err
	}
	code := s.signer.Code(docID, SHA256Hex([]byte(d.Raw)))
	return RenderTranscript(snap, code, s.verifyURL(code))
}

// VerifyResult doğrulama başarılı olduğunda belgenin kamuya gösterilen özetidir.
type VerifyResult struct {
	Valid      bool      `json:"valid"`
	DocumentID int64     `json:"document_id"`
	Kind       string    `json:"kind"`
	IssuedAt   time.Time `json:"issued_at"`
	FullName   string    `json:"full_name"`
	StudentNo  string    `json:"student_no"`
	Program    string    `json:"program"`
	GANO       float64   `json:"gano"`
	Courses    int       `json:"course_count"`
}

// Verify kodu doğrular. İmza, saklanan içeriğin ŞU ANKİ özetine göre kontrol edilir; ayrıca
// içeriğin kayıt sırasındaki özetle eşleşmesi aranır. Böylece veritabanında sonradan yapılan
// bir değişiklik (tetikleyiciler devre dışı bırakılsa bile) doğrulamayı başarısız kılar.
func (s *Service) Verify(ctx context.Context, code string) (VerifyResult, error) {
	id, sig, err := ParseCode(code)
	if err != nil {
		return VerifyResult{}, err
	}
	d, err := s.load(ctx, id)
	if err != nil {
		return VerifyResult{}, ErrInvalidCode // varlık bilgisini sızdırma
	}
	actual := SHA256Hex([]byte(d.Raw))
	if actual != d.SHA || !s.signer.Verify(id, sig, actual) {
		return VerifyResult{}, ErrInvalidCode
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(d.Raw), &snap); err != nil {
		return VerifyResult{}, ErrInvalidCode
	}
	return VerifyResult{
		Valid: true, DocumentID: id, Kind: snap.Kind, IssuedAt: snap.IssuedAt, FullName: snap.FullName,
		StudentNo: snap.StudentNo, Program: snap.Program, GANO: snap.GANO, Courses: len(snap.Results),
	}, nil
}

package documents

import (
	"bytes"
	_ "embed"
	"fmt"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/skip2/go-qrcode"
)

//go:embed fonts/DejaVuSans.ttf
var fontRegular []byte

//go:embed fonts/DejaVuSans-Bold.ttf
var fontBold []byte

// ResultRow transkriptteki bir ders satırıdır.
type ResultRow struct {
	Term   string  `json:"term"`
	Code   string  `json:"code"`
	Name   string  `json:"name"`
	ECTS   int     `json:"ects"`
	Letter string  `json:"letter"`
	Point  float64 `json:"point"`
	Passed bool    `json:"passed"`
	Exempt bool    `json:"exempt"`
}

// Snapshot belgenin düzenlendiği andaki içeriktir; imza bu içeriğin özetine bağlanır.
type Snapshot struct {
	Kind       string      `json:"kind"`
	IssuedAt   time.Time   `json:"issued_at"`
	StudentNo  string      `json:"student_no"`
	FullName   string      `json:"full_name"`
	Program    string      `json:"program"`
	Department string      `json:"department"`
	Faculty    string      `json:"faculty"`
	EntryYear  int         `json:"entry_year"`
	Results    []ResultRow `json:"results"`
	GANO       float64     `json:"gano"`
	PassedECTS int         `json:"passed_ects"`
}

// RenderTranscript snapshot'tan PDF üretir. verifyURL QR koda ve sayfa altına basılır.
func RenderTranscript(s Snapshot, code, verifyURL string) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCreationDate(s.IssuedAt)
	pdf.SetTitle("Transkript - "+s.StudentNo, true)
	pdf.AddUTF8FontFromBytes("dejavu", "", fontRegular)
	pdf.AddUTF8FontFromBytes("dejavu", "B", fontBold)
	pdf.SetMargins(15, 15, 15)
	pdf.SetAutoPageBreak(true, 25)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-18)
		pdf.SetFont("dejavu", "", 7)
		pdf.SetTextColor(90, 90, 90)
		pdf.MultiCell(0, 3.5, "Bu belge elektronik olarak imzalıdır. Doğruluğunu QR kodu okutarak veya aşağıdaki adresten kontrol ediniz:\n"+verifyURL, "", "L", false)
	})

	pdf.AddPage()
	pdf.SetFont("dejavu", "B", 16)
	pdf.SetTextColor(20, 20, 20)
	pdf.CellFormat(0, 9, "UniNet Üniversitesi — Transkript", "", 1, "L", false, 0, "")
	pdf.SetFont("dejavu", "", 9)
	pdf.CellFormat(0, 5, "Düzenlenme: "+s.IssuedAt.UTC().Format("02.01.2006 15:04 UTC"), "", 1, "L", false, 0, "")
	pdf.Ln(3)

	info := [][2]string{
		{"Öğrenci", s.FullName}, {"Öğrenci No", s.StudentNo}, {"Fakülte", s.Faculty},
		{"Bölüm", s.Department}, {"Program", s.Program}, {"Giriş Yılı", fmt.Sprint(s.EntryYear)},
	}
	for _, kv := range info {
		pdf.SetFont("dejavu", "B", 9)
		pdf.CellFormat(32, 5.5, kv[0], "", 0, "L", false, 0, "")
		pdf.SetFont("dejavu", "", 9)
		pdf.CellFormat(0, 5.5, kv[1], "", 1, "L", false, 0, "")
	}
	pdf.Ln(4)

	widths := []float64{24, 24, 78, 14, 14, 14}
	header := func() {
		pdf.SetFont("dejavu", "B", 8.5)
		pdf.SetFillColor(230, 230, 235)
		for i, h := range []string{"Dönem", "Kod", "Ders", "AKTS", "Not", "Puan"} {
			pdf.CellFormat(widths[i], 6.5, h, "1", 0, "L", true, 0, "")
		}
		pdf.Ln(-1)
	}
	header()
	pdf.SetFont("dejavu", "", 8.5)
	for _, r := range s.Results {
		if pdf.GetY() > 255 {
			pdf.AddPage()
			header()
			pdf.SetFont("dejavu", "", 8.5)
		}
		letter, point := r.Letter, fmt.Sprintf("%.2f", r.Point)
		if r.Exempt {
			letter, point = "MU", "-"
		}
		cells := []string{r.Term, r.Code, r.Name, fmt.Sprint(r.ECTS), letter, point}
		for i, c := range cells {
			pdf.CellFormat(widths[i], 6, c, "1", 0, "L", false, 0, "")
		}
		pdf.Ln(-1)
	}
	if len(s.Results) == 0 {
		pdf.CellFormat(sum(widths), 7, "Kayıtlı ders sonucu bulunmuyor.", "1", 1, "L", false, 0, "")
	}

	pdf.Ln(4)
	pdf.SetFont("dejavu", "B", 10)
	pdf.CellFormat(0, 6, fmt.Sprintf("GANO: %.2f     Başarılan AKTS: %d", s.GANO, s.PassedECTS), "", 1, "L", false, 0, "")

	// QR kod: doğrulama adresini taşır (imza adresin içindeki koddadır).
	png, err := qrcode.Encode(verifyURL, qrcode.Medium, 256)
	if err != nil {
		return nil, fmt.Errorf("qr: %w", err)
	}
	if pdf.GetY() > 225 {
		pdf.AddPage()
	}
	y := pdf.GetY() + 4
	opts := fpdf.ImageOptions{ImageType: "PNG", ReadDpi: false}
	pdf.RegisterImageOptionsReader("qr", opts, bytes.NewReader(png))
	pdf.ImageOptions("qr", 15, y, 32, 32, false, opts, 0, "")
	pdf.SetXY(52, y+6)
	pdf.SetFont("dejavu", "B", 9)
	pdf.CellFormat(0, 5, "Doğrulama kodu", "", 1, "L", false, 0, "")
	pdf.SetX(52)
	pdf.SetFont("dejavu", "", 7)
	pdf.MultiCell(0, 4, code, "", "L", false)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sum(xs []float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t
}

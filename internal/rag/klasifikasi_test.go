package rag

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// pencariPalsu mengembalikan kandidat tetap tanpa menyentuh Qdrant, dan
// mencatat perihal serta topK yang diterimanya.
type pencariPalsu struct {
	kandidat []Kandidat
	err      error

	perihal string
	topK    int
}

func (p *pencariPalsu) Cari(_ context.Context, perihal string, topK int) ([]Kandidat, error) {
	p.perihal = perihal
	p.topK = topK
	return p.kandidat, p.err
}

// cmRekam membungkus fakeChatModel dan mencatat berapa kali Stream dipanggil.
//
// Dipakai untuk membuktikan jalur tanpa kandidat tidak memanggil model sama
// sekali. fakeChatModel sengaja tidak diubah supaya test lain tidak ikut
// terpengaruh.
type cmRekam struct {
	fakeChatModel
	dipanggil int
}

func (c *cmRekam) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	c.dipanggil++
	return c.fakeChatModel.Stream(ctx, in, opts...)
}

// bacaAliran menguras aliran teks menjadi satu string.
func bacaAliran(t *testing.T, sr *schema.StreamReader[string]) string {
	t.Helper()
	defer sr.Close()

	var b strings.Builder
	for {
		potongan, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return b.String()
		}
		if err != nil {
			t.Fatalf("membaca aliran: %v", err)
		}
		b.WriteString(potongan)
	}
}

// TestPesanKodeMemuatPerihalDanKandidat mengunci isi prompt yang dikirim ke
// model: perihal apa adanya dan tiap kandidat lengkap dengan path dan skornya.
func TestPesanKodeMemuatPerihalDanKandidat(t *testing.T) {
	pesan := pesanKode("  permohonan cuti tahunan  ", []Kandidat{
		{
			Kode:   "090.1",
			Uraian: "Cuti tahunan",
			Path:   "KEPEGAWAIAN > CUTI > Cuti tahunan",
			Skor:   0.912,
		},
		{
			Kode:   "090.2",
			Uraian: "Cuti sakit",
			Path:   "KEPEGAWAIAN > CUTI > Cuti sakit",
			Skor:   0.734,
		},
	})

	for _, mau := range []string{
		"perihal naskah: permohonan cuti tahunan",
		"090.1 — Cuti tahunan",
		"path: KEPEGAWAIAN > CUTI > Cuti tahunan",
		"skor: 0.912",
		"090.2 — Cuti sakit",
		"skor: 0.734",
	} {
		if !strings.Contains(pesan, mau) {
			t.Errorf("pesan tidak memuat %q:\n%s", mau, pesan)
		}
	}
}

// titikSkor membuat satu titik hasil pencarian dengan payload kode/uraian/path.
func titikSkor(kode, uraian string, skor float32) *qdrantclient.ScoredPoint {
	return &qdrantclient.ScoredPoint{
		Payload: map[string]*qdrantclient.Value{
			kunciPayloadKode:   qdrantclient.NewValueString(kode),
			kunciPayloadUraian: qdrantclient.NewValueString(uraian),
			kunciPayloadPath:   qdrantclient.NewValueString("path " + kode),
		},
		Score: skor,
	}
}

// TestGabungPerKodeMenyimpanSkorTerbaik memastikan baris berkode sama digabung
// dan yang tersisa adalah kemunculan berskor tertinggi (yang paling awal).
//
// Tanpa penggabungan, satu kode bisa memakan beberapa slot kandidat, sehingga
// kode lain yang sebenarnya relevan tidak ikut ditampilkan ke model.
func TestGabungPerKodeMenyimpanSkorTerbaik(t *testing.T) {
	titik := []*qdrantclient.ScoredPoint{
		titikSkor("440.03.03.05", "Pengendalian Vektor", 0.90),
		titikSkor("440.03.03.05", "Penyehatan Lingkungan", 0.80),
		titikSkor("440.03.03.04", "Pengendalian Penyakit", 0.70),
	}

	hasil := gabungPerKode(titik, 10)
	if len(hasil) != 2 {
		t.Fatalf("dapat %d kandidat, mau 2 (kode kembar digabung): %+v", len(hasil), hasil)
	}
	if hasil[0].Kode != "440.03.03.05" || hasil[0].Skor != 0.90 {
		t.Errorf("kandidat pertama = %+v, mau kode 440.03.03.05 skor 0.90", hasil[0])
	}
	if hasil[0].Uraian != "Pengendalian Vektor" {
		t.Errorf("uraian = %q, mau kemunculan pertama (%q)", hasil[0].Uraian, "Pengendalian Vektor")
	}
	if hasil[1].Kode != "440.03.03.04" {
		t.Errorf("kandidat kedua = %q, mau 440.03.03.04", hasil[1].Kode)
	}
}

// TestGabungPerKodeMemotongKeMaks memastikan jumlah kandidat dibatasi maks.
func TestGabungPerKodeMemotongKeMaks(t *testing.T) {
	titik := []*qdrantclient.ScoredPoint{
		titikSkor("A", "a", 0.9),
		titikSkor("B", "b", 0.8),
		titikSkor("C", "c", 0.7),
	}

	if hasil := gabungPerKode(titik, 2); len(hasil) != 2 {
		t.Errorf("dapat %d kandidat, mau 2", len(hasil))
	}
}

// TestRekomendasiMeneruskanAliranModel menguji jalur bahagia: kandidat dicari
// dengan topK yang diberikan, lalu keluaran model diteruskan sebagai aliran.
func TestRekomendasiMeneruskanAliranModel(t *testing.T) {
	pencari := &pencariPalsu{kandidat: []Kandidat{{Kode: "090.1", Uraian: "Cuti tahunan", Skor: 0.9}}}
	cm := &cmRekam{fakeChatModel: fakeChatModel{chunks: []string{"Kode terbaik: ", "090.1 — Cuti tahunan"}}}

	pj := NewPenjawabKode(pencari, cm, 7)
	sr, err := pj.Rekomendasi(context.Background(), "permohonan cuti tahunan")
	if err != nil {
		t.Fatalf("Rekomendasi gagal: %v", err)
	}

	if got := bacaAliran(t, sr); got != "Kode terbaik: 090.1 — Cuti tahunan" {
		t.Errorf("jawaban = %q", got)
	}
	if pencari.topK != 7 {
		t.Errorf("topK diteruskan = %d, mau 7", pencari.topK)
	}
	if pencari.perihal != "permohonan cuti tahunan" {
		t.Errorf("perihal diteruskan = %q", pencari.perihal)
	}
	if cm.dipanggil != 1 {
		t.Errorf("model dipanggil %d kali, mau 1", cm.dipanggil)
	}
}

// TestRekomendasiKandidatKosongTidakMemanggilModel memastikan model tidak
// dipanggil saat tidak ada bahan untuk dipilih.
//
// Selain menghemat kuota, ini mencegah model mengarang kode yang tidak ada.
func TestRekomendasiKandidatKosongTidakMemanggilModel(t *testing.T) {
	pencari := &pencariPalsu{}
	cm := &cmRekam{}

	pj := NewPenjawabKode(pencari, cm, 10)
	sr, err := pj.Rekomendasi(context.Background(), "perihal apa pun")
	if err != nil {
		t.Fatalf("Rekomendasi gagal: %v", err)
	}

	got := bacaAliran(t, sr)
	if !strings.Contains(got, "Tidak ada kandidat") {
		t.Errorf("jawaban = %q, mau memuat pesan tidak ada kandidat", got)
	}
	if cm.dipanggil != 0 {
		t.Errorf("model dipanggil %d kali, mau 0", cm.dipanggil)
	}
}

// TestRekomendasiMeneruskanErrorPencari memastikan kegagalan pencarian
// dilaporkan, bukan diam-diam berubah menjadi jawaban kosong.
func TestRekomendasiMeneruskanErrorPencari(t *testing.T) {
	pencari := &pencariPalsu{err: errors.New("qdrant mati")}
	cm := &cmRekam{}

	pj := NewPenjawabKode(pencari, cm, 10)
	if _, err := pj.Rekomendasi(context.Background(), "perihal"); err == nil {
		t.Fatal("Rekomendasi berhasil, mau error dari pencari")
	}
	if cm.dipanggil != 0 {
		t.Errorf("model dipanggil %d kali, mau 0", cm.dipanggil)
	}
}

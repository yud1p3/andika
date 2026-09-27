package ingest

import (
	"math"
	"testing"
)

// TestParseVektorPg mengunci cara membaca vektor dari teks pgvector.
//
// Bentuk teks ini yang benar-benar keluar dari "embedding::text", jadi
// perubahannya — misalnya spasi setelah koma, notasi ilmiah, atau vektor
// kosong — harus tetap terbaca tanpa mengubah nilainya.
func TestParseVektorPg(t *testing.T) {
	cases := []struct {
		nama  string
		teks  string
		want  []float32
		gagal bool
	}{
		{nama: "biasa", teks: "[0.1,0.2,0.3]", want: []float32{0.1, 0.2, 0.3}},
		{nama: "ada spasi", teks: "[0.1, 0.2, 0.3]", want: []float32{0.1, 0.2, 0.3}},
		{nama: "spasi di ujung", teks: "  [1,-2]  ", want: []float32{1, -2}},
		{nama: "notasi ilmiah", teks: "[1e-3,2.5e-1]", want: []float32{0.001, 0.25}},
		{nama: "kosong", teks: "[]", want: nil},
		{nama: "tanpa kurung", teks: "0.1,0.2", gagal: true},
		{nama: "angka rusak", teks: "[0.1,x,0.3]", gagal: true},
		{nama: "teks kosong", teks: "", gagal: true},
	}

	for _, c := range cases {
		t.Run(c.nama, func(t *testing.T) {
			got, err := parseVektorPg(c.teks)
			if c.gagal {
				if err == nil {
					t.Fatalf("parseVektorPg(%q) berhasil, mau error", c.teks)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVektorPg(%q) gagal: %v", c.teks, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("panjang = %d, mau %d", len(got), len(c.want))
			}
			for i := range got {
				if math.Abs(float64(got[i]-c.want[i])) > 1e-6 {
					t.Errorf("elemen %d = %v, mau %v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// TestTitikKodeMemakaiKunciUnik mengunci dua sifat yang membuat impor benar:
// ID titik berasal dari kolom kunci unik (id) sehingga impor ulang menimpa
// titik yang sama, dan dua baris berkode sama tetap jadi titik berbeda.
//
// Sifat kedua yang paling penting: 12 kode di data nyata dipakai lebih dari
// satu item, jadi memakai kode sebagai ID akan membuatnya saling menimpa.
func TestTitikKodeMemakaiKunciUnik(t *testing.T) {
	b := barisKode{
		ID:     184497,
		Kode:   "523.02.02.01.01",
		Uraian: "rancang bangun kapal perikanan",
		Path:   "KELAUTAN DAN PERIKANAN > PERIKANAN TANGKAP > rancang bangun kapal perikanan",
		Vektor: []float32{0.1, 0.2},
	}

	satu := titikKode(b)
	dua := titikKode(b)

	idSatu := satu.GetId().GetNum()
	if idSatu == 0 {
		t.Fatal("ID titik kosong, mau nomor dari kolom id")
	}
	if idSatu != dua.GetId().GetNum() {
		t.Errorf("ID berubah antar-pemanggilan: %d vs %d", idSatu, dua.GetId().GetNum())
	}

	// Kode sama, id berbeda: harus tetap dua titik berbeda.
	lain := titikKode(barisKode{ID: 184498, Kode: b.Kode, Vektor: b.Vektor})
	if lain.GetId().GetNum() == idSatu {
		t.Error("id berbeda menghasilkan titik yang sama")
	}

	if got := satu.GetPayload()[kunciPayloadKode].GetStringValue(); got != b.Kode {
		t.Errorf("payload %s = %q, mau %q", kunciPayloadKode, got, b.Kode)
	}
	if got := satu.GetPayload()[kunciPayloadUraian].GetStringValue(); got != b.Uraian {
		t.Errorf("payload %s = %q, mau %q", kunciPayloadUraian, got, b.Uraian)
	}
	if got := satu.GetPayload()[kunciPayloadPath].GetStringValue(); got != b.Path {
		t.Errorf("payload %s = %q, mau %q", kunciPayloadPath, got, b.Path)
	}
	if got := satu.GetVectors().GetVector().GetDense().GetData(); len(got) != 2 {
		t.Errorf("panjang vektor tersimpan = %d, mau 2", len(got))
	}
}

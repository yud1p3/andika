package rag

import (
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// TestLabelSumberMengutamakanPasal memastikan potongan berpasal ditandai
// dengan nomor pasalnya — itu yang dicari pembaca peraturan.
func TestLabelSumberMengutamakanPasal(t *testing.T) {
	doc := dokSkor(t, "Pasal 7 ...", 0.9, map[string]any{
		"bagian": "batang_tubuh",
		"pasal":  "7",
	})

	if got := labelSumber(doc); got != "Pasal 7" {
		t.Errorf("labelSumber() = %q, mau %q", got, "Pasal 7")
	}
}

// TestLabelSumberJatuhKeNamaBagian menutup potongan yang tidak berpasal.
//
// Pembukaan dan Lampiran tidak punya nomor pasal, jadi tanpa cabang ini
// rujukannya akan tampil kosong walaupun potongannya benar-benar dipakai.
func TestLabelSumberJatuhKeNamaBagian(t *testing.T) {
	for bagian, mau := range map[string]string{
		"lampiran":    "Lampiran",
		"pembukaan":   "Pembukaan",
		"tanpa_pasal": "Dokumen",
	} {
		doc := dokSkor(t, "isi", 0.5, map[string]any{"bagian": bagian})
		if got := labelSumber(doc); got != mau {
			t.Errorf("bagian %q: labelSumber() = %q, mau %q", bagian, got, mau)
		}
	}
}

// TestLabelSumberTanpaMetadata menjaga dokumen non-peraturan.
//
// Tanpa metadata sama sekali, bacaMetadata mengembalikan nil. Pembacaan peta
// nil aman di Go, tapi hasilnya harus tetap punya label yang masuk akal,
// bukan string kosong yang tampil sebagai chip hampa di halaman.
func TestLabelSumberTanpaMetadata(t *testing.T) {
	doc := dokSkor(t, "isi apa saja", 0.5, nil)

	if got := labelSumber(doc); got != "Dokumen" {
		t.Errorf("labelSumber() = %q, mau %q", got, "Dokumen")
	}
}

// TestLabelSumberMembuangKembar mengunci dua hal sekaligus: satu pasal yang
// terpecah menjadi beberapa potongan hanya muncul sekali, dan urutannya tetap
// mengikuti peringkat retrieval.
func TestLabelSumberMembuangKembar(t *testing.T) {
	docs := []*schema.Document{
		dokSkor(t, "Pasal 89 bagian 1", 0.9, map[string]any{"bagian": "batang_tubuh", "pasal": "89"}),
		dokSkor(t, "Pasal 7 ...", 0.8, map[string]any{"bagian": "batang_tubuh", "pasal": "7"}),
		dokSkor(t, "Pasal 89 bagian 2", 0.7, map[string]any{"bagian": "batang_tubuh", "pasal": "89"}),
		dokSkor(t, "Pasal 89 bagian 3", 0.6, map[string]any{"bagian": "batang_tubuh", "pasal": "89"}),
	}

	got := LabelSumber(docs)
	mau := []string{"Pasal 89", "Pasal 7"}

	if !reflect.DeepEqual(got, mau) {
		t.Errorf("LabelSumber() = %v, mau %v", got, mau)
	}
}

// TestLabelSumberKosong memastikan tidak ada label yang dihasilkan dari
// retrieval kosong, supaya pemanggil bisa memakai panjangnya sebagai penanda
// "tidak ada yang bisa dirujuk".
func TestLabelSumberKosong(t *testing.T) {
	if got := LabelSumber(nil); len(got) != 0 {
		t.Errorf("LabelSumber(nil) = %v, mau kosong", got)
	}
}

// TestSaringSumberMenyisakanYangDisebut mengunci kasus yang memicu fitur ini:
// jejak agen memuat banyak pasal, tetapi jawabannya hanya bersumber dari satu.
func TestSaringSumberMenyisakanYangDisebut(t *testing.T) {
	label := []string{"Pasal 7", "Pasal 8", "Pasal 145", "Pasal 94"}

	got := SaringSumber(label, "Berdasarkan Pasal 8, Naskah Dinas arahan terdiri atas tiga jenis.")
	mau := []string{"Pasal 8"}

	if !reflect.DeepEqual(got, mau) {
		t.Errorf("SaringSumber() = %v, mau %v", got, mau)
	}
}

// TestSaringSumberTidakTertukarNomorSerupa menjaga batas kata pada nomor
// pasal: jawaban yang menyebut "Pasal 89" tidak boleh dianggap menyebut
// "Pasal 8".
func TestSaringSumberTidakTertukarNomorSerupa(t *testing.T) {
	if got := SaringSumber([]string{"Pasal 8"}, "sebagaimana dimaksud dalam Pasal 89"); len(got) != 0 {
		t.Errorf("SaringSumber() = %v, mau kosong", got)
	}

	if got := SaringSumber([]string{"Pasal 8"}, "lihat Pasal 8 ayat (2)"); len(got) != 1 {
		t.Errorf("SaringSumber() = %v, mau [Pasal 8]", got)
	}
}

// TestSaringSumberTidakPekaHurufBesarKecil memastikan penyebutan pasal di
// tengah kalimat tetap dikenali.
func TestSaringSumberTidakPekaHurufBesarKecil(t *testing.T) {
	if got := SaringSumber([]string{"Pasal 7"}, "menurut pasal 7, surat kuasa..."); len(got) != 1 {
		t.Errorf("SaringSumber() = %v, mau [Pasal 7]", got)
	}
}

// TestSaringSumberLabelNonPasal menutup label yang tidak punya nomor: ia lolos
// hanya kalau bagiannya benar-benar disebut di jawaban.
func TestSaringSumberLabelNonPasal(t *testing.T) {
	label := []string{"Lampiran", "Pembukaan", "Dokumen"}

	got := SaringSumber(label, "Formatnya dijelaskan di Lampiran.")
	mau := []string{"Lampiran"}

	if !reflect.DeepEqual(got, mau) {
		t.Errorf("SaringSumber() = %v, mau %v", got, mau)
	}
}

// TestSaringSumberMenjagaUrutan memastikan penyaringan tidak mengubah urutan
// peringkat retrieval.
func TestSaringSumberMenjagaUrutan(t *testing.T) {
	label := []string{"Pasal 7", "Pasal 8", "Pasal 9"}

	got := SaringSumber(label, "Pasal 9 dan Pasal 7 saling berkaitan.")
	mau := []string{"Pasal 7", "Pasal 9"}

	if !reflect.DeepEqual(got, mau) {
		t.Errorf("SaringSumber() = %v, mau %v", got, mau)
	}
}

// TestSaringSumberKosong menutup dua masukan kosong: tanpa label dan tanpa
// jawaban. Keduanya berarti tidak ada yang bisa dirujuk.
func TestSaringSumberKosong(t *testing.T) {
	if got := SaringSumber(nil, "jawaban apa saja"); len(got) != 0 {
		t.Errorf("SaringSumber(nil, ...) = %v, mau kosong", got)
	}
	if got := SaringSumber([]string{"Pasal 7"}, "   "); len(got) != 0 {
		t.Errorf("SaringSumber(..., kosong) = %v, mau kosong", got)
	}
}

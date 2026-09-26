package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rag-eino/internal/gemini"
)

// cmSaran merakit chat model palsu yang siap dipakai Saran.
//
// Dibungkus dalam *gemini.Client supaya jalur yang diuji sama dengan jalur
// sungguhan: Saran menerima model.BaseChatModel, bukan tipe palsu.
func cmSaran(f *fakeChatModel) *gemini.Client {
	return &gemini.Client{ChatModel: f, DocEmbedder: fakeEmbedder{}, QueryEmbedder: fakeEmbedder{}}
}

// TestSaranMenyusunPertanyaanLanjutan menguji jalur bahagia: keluaran model
// yang sudah bersih diteruskan apa adanya, terurut.
func TestSaranMenyusunPertanyaanLanjutan(t *testing.T) {
	cm := &fakeChatModel{saranReply: "Apa syarat pengajuan naskah dinas?\nBerapa lama prosesnya?\nSiapa yang menandatangani?"}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel,
		"bagaimana penomoran naskah dinas?", "Pasal 7 mengatur jenis naskah dinas.", 3)
	if err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	mau := []string{
		"Apa syarat pengajuan naskah dinas?",
		"Berapa lama prosesnya?",
		"Siapa yang menandatangani?",
	}
	if len(hasil) != len(mau) {
		t.Fatalf("dapat %d saran %q, mau %d", len(hasil), hasil, len(mau))
	}
	for i := range mau {
		if hasil[i] != mau[i] {
			t.Errorf("saran ke-%d = %q, mau %q", i, hasil[i], mau[i])
		}
	}

	if n := cm.jumlahSaran(); n != 1 {
		t.Errorf("model dipanggil %d kali untuk saran, mau 1", n)
	}
}

// TestSaranMembuangPenomoranDanTandaKutip memastikan keluaran model yang
// "kotor" tetap bisa dipakai.
//
// Model sering menambahkan penomoran, bullet, dan tanda kutip meski sudah
// dilarang di instruksi. Membuangnya di sini jauh lebih murah daripada
// memaksa model mematuhi format lewat percobaan ulang.
func TestSaranMembuangPenomoranDanTandaKutip(t *testing.T) {
	cm := &fakeChatModel{saranReply: strings.Join([]string{
		`1. Apa syaratnya?`,
		`- "Berapa lama prosesnya?"`,
		`• Siapa penandatangannya?`,
		`2) Apa sanksi keterlambatan?`,
	}, "\n")}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, "tanya", "konteks", 4)
	if err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	mau := []string{
		"Apa syaratnya?",
		"Berapa lama prosesnya?",
		"Siapa penandatangannya?",
		"Apa sanksi keterlambatan?",
	}
	if len(hasil) != len(mau) {
		t.Fatalf("dapat %q, mau %q", hasil, mau)
	}
	for i := range mau {
		if hasil[i] != mau[i] {
			t.Errorf("saran ke-%d = %q, mau %q", i, hasil[i], mau[i])
		}
	}
}

// TestSaranMembuangBarisKosongDanDuplikat menjaga daftar tetap ringkas.
func TestSaranMembuangBarisKosongDanDuplikat(t *testing.T) {
	cm := &fakeChatModel{saranReply: strings.Join([]string{
		"Apa syaratnya?",
		"",
		"   ",
		"Apa Syaratnya?", // duplikat, beda kapitalisasi
		"Berapa lama?",
		"Apa   syaratnya?", // duplikat, beda spasi
	}, "\n")}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, "tanya", "konteks", 5)
	if err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	mau := []string{"Apa syaratnya?", "Berapa lama?"}
	if len(hasil) != len(mau) {
		t.Fatalf("dapat %q, mau %q", hasil, mau)
	}
	for i := range mau {
		if hasil[i] != mau[i] {
			t.Errorf("saran ke-%d = %q, mau %q", i, hasil[i], mau[i])
		}
	}
}

// TestSaranMembatasiJumlah memastikan SARAN_COUNT dihormati walau model
// memberi lebih banyak.
func TestSaranMembatasiJumlah(t *testing.T) {
	cm := &fakeChatModel{saranReply: strings.Join([]string{
		"satu?", "dua?", "tiga?", "empat?", "lima?",
	}, "\n")}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, "tanya", "konteks", 2)
	if err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	if len(hasil) != 2 {
		t.Fatalf("dapat %d saran %q, mau 2", len(hasil), hasil)
	}
	if hasil[0] != "satu?" || hasil[1] != "dua?" {
		t.Errorf("saran = %q, mau dua yang pertama", hasil)
	}
}

// TestSaranTidakMengulangPertanyaanPengguna menjaga saran tidak menawarkan
// pertanyaan yang baru saja diajukan.
func TestSaranTidakMengulangPertanyaanPengguna(t *testing.T) {
	const pengguna = "Bagaimana penomoran naskah dinas?"

	cm := &fakeChatModel{saranReply: strings.Join([]string{
		pengguna,
		"  bagaimana   penomoran NASKAH DINAS?  ", // bentuk lain, tetap harus dibuang
		"Siapa yang berwenang menomori?",
	}, "\n")}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, pengguna, "konteks", 5)
	if err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	mau := []string{"Siapa yang berwenang menomori?"}
	if len(hasil) != len(mau) || hasil[0] != mau[0] {
		t.Errorf("saran = %q, mau %q", hasil, mau)
	}
}

// TestSaranMembuangBarisPembukaBerakhiranTitikDua membuang basa-basi pembuka
// yang sering ditambahkan model.
func TestSaranMembuangBarisPembukaBerakhiranTitikDua(t *testing.T) {
	cm := &fakeChatModel{saranReply: strings.Join([]string{
		"Berikut beberapa pertanyaan lanjutan yang mungkin berguna:",
		"Apa syaratnya?",
	}, "\n")}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, "tanya", "konteks", 3)
	if err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	if len(hasil) != 1 || hasil[0] != "Apa syaratnya?" {
		t.Errorf("saran = %q, mau hanya %q", hasil, "Apa syaratnya?")
	}
}

// TestSaranMematikanSaatJumlahNol memastikan SARAN_COUNT=0 tidak memanggil
// model sama sekali — bukan sekadar membuang hasilnya.
func TestSaranMematikanSaatJumlahNol(t *testing.T) {
	kasus := []struct {
		nama       string
		jumlah     int
		konteks    string
		pertanyaan string
	}{
		{"jumlah nol", 0, "konteks", "tanya"},
		{"jumlah negatif", -1, "konteks", "tanya"},
		{"konteks kosong", 3, "", "tanya"},
		{"konteks hanya spasi", 3, "   \n  ", "tanya"},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			cm := &fakeChatModel{saranReply: "tidak boleh dipakai"}

			hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, k.pertanyaan, k.konteks, k.jumlah)
			if err != nil {
				t.Fatalf("menyusun saran: %v", err)
			}
			if hasil != nil {
				t.Errorf("hasil = %q, mau nil", hasil)
			}
			if n := cm.jumlahSaran(); n != 0 {
				t.Errorf("model dipanggil %d kali, mau 0", n)
			}
		})
	}
}

// TestSaranGagalMengembalikanError memastikan kegagalan model tidak diam-diam
// jadi daftar kosong, supaya pemanggil bisa mencatatnya.
func TestSaranGagalMengembalikanError(t *testing.T) {
	cm := &fakeChatModel{saranErr: errors.New("kuota habis")}

	hasil, err := Saran(context.Background(), cmSaran(cm).ChatModel, "tanya", "konteks", 3)
	if err == nil {
		t.Fatal("error nil, mau error dari model")
	}
	if hasil != nil {
		t.Errorf("hasil = %q, mau nil saat error", hasil)
	}
	if !strings.Contains(err.Error(), "kuota habis") {
		t.Errorf("error = %v, mau memuat penyebab asli", err)
	}
}

// TestSaranMemakaiPertanyaanDanKonteks memastikan kedua masukan benar-benar
// sampai ke model.
func TestSaranMemakaiPertanyaanDanKonteks(t *testing.T) {
	const (
		pertanyaan = "bagaimana penomoran naskah dinas?"
		konteks    = "Pasal 7 mengatur jenis naskah dinas."
	)

	cm := &fakeChatModel{saranReply: "Apa syaratnya?"}

	if _, err := Saran(context.Background(), cmSaran(cm).ChatModel, pertanyaan, konteks, 3); err != nil {
		t.Fatalf("menyusun saran: %v", err)
	}

	if len(cm.panggilan) != 1 {
		t.Fatalf("model dipanggil %d kali, mau 1", len(cm.panggilan))
	}
	pesan := cm.panggilan[0]
	if len(pesan) != 2 {
		t.Fatalf("panggilan berisi %d pesan, mau 2 (sistem + user)", len(pesan))
	}
	if !strings.HasPrefix(pesan[0].Content, saranInstructionAwal) {
		t.Errorf("pesan sistem tidak memakai instruksi saran:\n%s", pesan[0].Content)
	}
	if !strings.Contains(pesan[1].Content, pertanyaan) {
		t.Errorf("pesan user tidak memuat pertanyaan:\n%s", pesan[1].Content)
	}
	if !strings.Contains(pesan[1].Content, konteks) {
		t.Errorf("pesan user tidak memuat konteks:\n%s", pesan[1].Content)
	}
}

// TestSaranInstruksiMemuatJumlah memaku jumlah yang diminta ikut tertulis di
// instruksi, karena itu satu-satunya cara memberi tahu model berapa yang mau.
func TestSaranInstruksiMemuatJumlah(t *testing.T) {
	if got := saranInstruction(4); !strings.Contains(got, "4 pertanyaan") {
		t.Errorf("instruksi tidak menyebut jumlah:\n%s", got)
	}
}

// TestUraiSaranMengembalikanNilSaatTidakAdaIsi menjaga keluaran kosong tidak
// jadi slice berisi string kosong.
func TestUraiSaranMengembalikanNilSaatTidakAdaIsi(t *testing.T) {
	for _, teks := range []string{"", "\n\n", "   \n  ", "Pembuka:", "- \n* \n"} {
		if got := uraiSaran(teks, "tanya", 3); len(got) != 0 {
			t.Errorf("uraiSaran(%q) = %q, mau kosong", teks, got)
		}
	}
}

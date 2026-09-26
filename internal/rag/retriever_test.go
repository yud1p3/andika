package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// retrieverRekam menjawab dua kali dengan daftar berbeda, dan mencatat setiap
// panggilan.
//
// Percobaan pertama dianggap pencarian vektor biasa, percobaan kedua pencarian
// per pasal. Pembedaan itu meniru perilaku retriever Qdrant, yang membedakan
// keduanya hanya lewat opsi filter yang diterimanya.
type retrieverRekam struct {
	hasilVektor []*schema.Document
	hasilPasal  []*schema.Document

	errVektor error
	errPasal  error

	queries []string
	topK    []int
	panggil int
}

func (r *retrieverRekam) Retrieve(_ context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	r.panggil++
	r.queries = append(r.queries, query)

	acuan := topKAcuan
	co := retriever.GetCommonOptions(&retriever.Options{TopK: &acuan}, opts...)
	r.topK = append(r.topK, *co.TopK)

	if r.panggil == 1 {
		return r.hasilVektor, r.errVektor
	}
	return r.hasilPasal, r.errPasal
}

// dok sederhana dengan ID tetap, supaya penggabungan bisa diperiksa.
func dok(id string) *schema.Document {
	return &schema.Document{
		ID:       id,
		Content:  "isi " + id,
		MetaData: map[string]any{},
	}
}

func TestNomorPasalDirujuk(t *testing.T) {
	for nama, kasus := range map[string]struct {
		query string
		mau   []string
	}{
		"tanpa rujukan":            {"apa saja jenis naskah dinas", nil},
		"huruf kecil":              {"pasal 1 membahas apa", []string{"1"}},
		"huruf besar":              {"Pasal 139 mengatur apa", []string{"139"}},
		"di tengah kalimat":        {"apa isi pasal 12 peraturan ini", []string{"12"}},
		"dengan ayat":              {"pasal 89 ayat (2) berbunyi apa", []string{"89"}},
		"dua rujukan":              {"apa bedanya pasal 1 dan pasal 5", []string{"1", "5"}},
		"rujukan kembar disatukan": {"pasal 3 lalu pasal 3 lagi", []string{"3"}},
		"kata pasal tanpa nomor":   {"apa itu pasal", nil},
		"angka tanpa kata pasal":   {"apa isi 12 peraturan ini", nil},
		// Kata yang memuat "pasal" tidak boleh ikut tertangkap.
		"kata lain yang memuat pasal": {"pasalnya bagaimana", nil},
	} {
		t.Run(nama, func(t *testing.T) {
			got := nomorPasalDirujuk(kasus.query)
			if strings.Join(got, ",") != strings.Join(kasus.mau, ",") {
				t.Errorf("nomorPasalDirujuk(%q) = %v, mau %v", kasus.query, got, kasus.mau)
			}
		})
	}
}

// TestFilterPasal mengunci bentuk filter Qdrant.
//
// Dua hal mudah salah di sini. Pertama, alamat bidangnya: metadata tidak
// disimpan sebagai kunci tingkat atas, melainkan di bawah kunci "metadata",
// jadi alamatnya "metadata.pasal" — bukan "pasal". Salah alamat berarti filter
// tidak menyaring apa pun, dan gagalnya diam-diam karena Qdrant hanya
// mengembalikan nol hasil, bukan error.
//
// Kedua, beberapa nomor harus digabung dengan Should (ATAU), bukan Must
// (DAN): pertanyaan "apa bedanya pasal 1 dan pasal 5" tidak akan pernah
// terpenuhi kalau keduanya harus cocok sekaligus.
func TestFilterPasal(t *testing.T) {
	f := filterPasal([]string{"1", "5"})

	if f == nil {
		t.Fatal("filterPasal() = nil")
	}
	if len(f.Must) != 0 {
		t.Errorf("Must berisi %d kondisi, mau kosong — beberapa pasal digabung dengan Should", len(f.Must))
	}
	if len(f.Should) != 2 {
		t.Fatalf("Should berisi %d kondisi, mau 2", len(f.Should))
	}

	for i, mau := range []string{"1", "5"} {
		bidang := f.Should[i].GetField()
		if bidang == nil {
			t.Fatalf("kondisi %d bukan kondisi bidang", i)
		}
		if bidang.GetKey() != bidangPasal {
			t.Errorf("kondisi %d: bidang = %q, mau %q", i, bidang.GetKey(), bidangPasal)
		}
		if got := bidang.GetMatch().GetKeyword(); got != mau {
			t.Errorf("kondisi %d: nilai = %q, mau %q", i, got, mau)
		}
	}
}

// TestBidangPasalMemakaiAlamatPayload mengunci alamat bidang secara terpisah,
// supaya kesalahan alamat gagal di sini dengan pesan yang jelas — bukan di
// test lain yang gejalanya cuma "tidak ada hasil".
func TestBidangPasalMemakaiAlamatPayload(t *testing.T) {
	if bidangPasal != "metadata.pasal" {
		t.Errorf("bidangPasal = %q, mau %q", bidangPasal, "metadata.pasal")
	}
}

// TestRetrieverPasalMenambahPotonganPasal adalah perilaku inti perbaikan ini.
//
// Pertanyaan yang menyebut nomor pasal harus mengambil seluruh potongan pasal
// itu, lalu meletakkannya di depan hasil vektor.
func TestRetrieverPasalMenambahPotonganPasal(t *testing.T) {
	rekam := &retrieverRekam{
		hasilVektor: []*schema.Document{dok("v1"), dok("v2")},
		hasilPasal:  []*schema.Document{dok("p1"), dok("p2"), dok("p3")},
	}
	r := newRetrieverPasal(rekam)

	hasil, err := r.Retrieve(context.Background(), "pasal 1 membahas apa")
	if err != nil {
		t.Fatalf("Retrieve() gagal: %v", err)
	}

	if rekam.panggil != 2 {
		t.Fatalf("retriever dipanggil %d kali, mau 2 (vektor lalu pasal)", rekam.panggil)
	}

	mau := []string{"p1", "p2", "p3", "v1", "v2"}
	if got := idDokumen(hasil); strings.Join(got, ",") != strings.Join(mau, ",") {
		t.Errorf("urutan hasil = %v, mau %v (potongan pasal di depan)", got, mau)
	}

	// Percobaan kedua harus memakai topK khusus, bukan topK pemanggil.
	if rekam.topK[0] != topKAcuan {
		t.Errorf("percobaan vektor memakai topK %d, mau %d", rekam.topK[0], topKAcuan)
	}
	if rekam.topK[1] != topKPasal {
		t.Errorf("percobaan pasal memakai topK %d, mau %d", rekam.topK[1], topKPasal)
	}
}

// TestRetrieverPasalMenandaiAsal memastikan asal potongan bisa dibedakan.
//
// Tanpa penanda ini, potongan berlabel "pasal" tampak seperti hasil vektor
// berskor rendah — padahal ia ada di situ justru karena nomor pasalnya disebut,
// dan skornya yang rendah bukan tanda keliru.
func TestRetrieverPasalMenandaiAsal(t *testing.T) {
	rekam := &retrieverRekam{
		hasilVektor: []*schema.Document{dok("v1")},
		hasilPasal:  []*schema.Document{dok("p1")},
	}
	r := newRetrieverPasal(rekam)

	hasil, err := r.Retrieve(context.Background(), "pasal 1 membahas apa")
	if err != nil {
		t.Fatalf("Retrieve() gagal: %v", err)
	}

	mau := map[string]string{"p1": asalPasal, "v1": asalVektor}
	for _, d := range hasil {
		if got := bacaAsal(d); got != mau[d.ID] {
			t.Errorf("potongan %s: asal = %q, mau %q", d.ID, got, mau[d.ID])
		}
	}
}

// TestRetrieverPasalTidakMenggandakanPotongan menjaga potongan yang ditemukan
// kedua cara.
//
// Potongan yang sudah diambil lewat penyaringan pasal tidak perlu muncul lagi
// sebagai hasil vektor: isinya sama, dan hanya memakan slot TOP_K.
func TestRetrieverPasalTidakMenggandakanPotongan(t *testing.T) {
	rekam := &retrieverRekam{
		hasilVektor: []*schema.Document{dok("sama"), dok("v1")},
		hasilPasal:  []*schema.Document{dok("sama"), dok("p1")},
	}
	r := newRetrieverPasal(rekam)

	hasil, err := r.Retrieve(context.Background(), "pasal 1 membahas apa")
	if err != nil {
		t.Fatalf("Retrieve() gagal: %v", err)
	}

	if got := idDokumen(hasil); strings.Join(got, ",") != "sama,p1,v1" {
		t.Errorf("hasil = %v, mau [sama p1 v1]", got)
	}

	// Potongan yang ditemukan kedua cara dihitung sebagai hasil penyaringan:
	// memang itu yang membuatnya terambil lebih dulu.
	for _, d := range hasil {
		if d.ID == "sama" && bacaAsal(d) != asalPasal {
			t.Errorf("potongan \"sama\": asal = %q, mau %q", bacaAsal(d), asalPasal)
		}
	}
}

// TestRetrieverPasalTanpaRujukan memastikan pertanyaan biasa tidak berubah
// perilakunya sama sekali.
//
// Ini yang menjaga agar perbaikan ini tidak diam-diam mengubah hasil pencarian
// untuk pertanyaan yang tidak menyebut pasal apa pun.
func TestRetrieverPasalTanpaRujukan(t *testing.T) {
	rekam := &retrieverRekam{
		hasilVektor: []*schema.Document{dok("v1")},
		hasilPasal:  []*schema.Document{dok("p1")},
	}
	r := newRetrieverPasal(rekam)

	hasil, err := r.Retrieve(context.Background(), "apa saja jenis naskah dinas")
	if err != nil {
		t.Fatalf("Retrieve() gagal: %v", err)
	}

	if rekam.panggil != 1 {
		t.Errorf("retriever dipanggil %d kali, mau 1 (tanpa penyaringan)", rekam.panggil)
	}
	if got := idDokumen(hasil); strings.Join(got, ",") != "v1" {
		t.Errorf("hasil = %v, mau [v1]", got)
	}
	if bacaAsal(hasil[0]) != "" {
		t.Errorf("asal = %q, mau kosong — tanpa penyaringan tidak ada yang perlu ditandai", bacaAsal(hasil[0]))
	}
}

// TestRetrieverPasalHasilKosongDilewati menjaga nomor pasal yang tidak ada.
//
// Pertanyaan bisa menyebut pasal dari peraturan lain. Penyaringan yang tidak
// menemukan apa pun tidak boleh membuat seluruh pencarian kosong — dan tidak
// boleh pula menandai hasilnya, karena tidak ada penyaringan yang terjadi.
func TestRetrieverPasalHasilKosongDilewati(t *testing.T) {
	rekam := &retrieverRekam{
		hasilVektor: []*schema.Document{dok("v1")},
		hasilPasal:  nil,
	}
	r := newRetrieverPasal(rekam)

	hasil, err := r.Retrieve(context.Background(), "pasal 999 mengatur apa")
	if err != nil {
		t.Fatalf("Retrieve() gagal: %v", err)
	}

	if got := idDokumen(hasil); strings.Join(got, ",") != "v1" {
		t.Errorf("hasil = %v, mau [v1] (jatuh kembali ke hasil vektor)", got)
	}
	if got := bacaAsal(hasil[0]); got != "" {
		t.Errorf("asal = %q, mau kosong — penyaringan tidak menyumbang apa pun", got)
	}
}

// TestRetrieverPasalErrorDisaringDilewati menjaga kegagalan penyaringan.
//
// Hasil vektor sudah cukup untuk menjawab banyak pertanyaan, jadi kegagalan
// penyaringan tidak boleh menjatuhkan seluruh pencarian.
func TestRetrieverPasalErrorDisaringDilewati(t *testing.T) {
	rekam := &retrieverRekam{
		hasilVektor: []*schema.Document{dok("v1")},
		errPasal:    errors.New("filter ditolak Qdrant"),
	}
	r := newRetrieverPasal(rekam)

	hasil, err := r.Retrieve(context.Background(), "pasal 1 membahas apa")
	if err != nil {
		t.Fatalf("Retrieve() mengembalikan error: %v; mau jatuh kembali ke hasil vektor", err)
	}
	if got := idDokumen(hasil); strings.Join(got, ",") != "v1" {
		t.Errorf("hasil = %v, mau [v1]", got)
	}
}

// TestRetrieverPasalErrorVektorDiteruskan menjaga kegagalan pencarian utama.
//
// Kalau pencarian vektor sendiri gagal, tidak ada yang bisa dipakai sebagai
// cadangan — errornya harus diteruskan, bukan ditelan.
func TestRetrieverPasalErrorVektorDiteruskan(t *testing.T) {
	rekam := &retrieverRekam{errVektor: errors.New("Qdrant tidak bisa dihubungi")}
	r := newRetrieverPasal(rekam)

	if _, err := r.Retrieve(context.Background(), "pasal 1 membahas apa"); err == nil {
		t.Fatal("Retrieve() tidak mengembalikan error, mau error dari pencarian vektor")
	}
	if rekam.panggil != 1 {
		t.Errorf("retriever dipanggil %d kali, mau 1 (berhenti setelah pencarian vektor gagal)", rekam.panggil)
	}
}

// TestRetrieverPasalTidakMengubahOpsiPemanggil menjaga opsi milik pemanggil.
//
// Opsi tambahan ditulis ke slice baru. Kalau ditulis ke slice pemanggil,
// "rag cari -topk 60" akan diam-diam berubah menjadi topK pasal, dan pengguna
// kehilangan kemampuan memeriksa seberapa dalam hasil pencarian sebenarnya.
//
// Membuktikannya tidak bisa lewat len(opts): append yang menulis ke array
// bersama tidak mengubah panjang slice pemanggil. Yang berubah adalah isi
// array di luar panjang itu — dan itu baru terlihat oleh slice lain yang
// berbagi array yang sama. Karena itu di sini dibuat "tetangga" dengan panjang
// lebih besar dari opts, lalu slot di luar jangkauan opts diperiksa setelah
// pemanggilan.
func TestRetrieverPasalTidakMengubahOpsiPemanggil(t *testing.T) {
	rekam := &retrieverRekam{hasilPasal: []*schema.Document{dok("p1")}}
	r := newRetrieverPasal(rekam)

	// Kapasitas sengaja dilebihkan: tanpa kapasitas sisa, append akan
	// mengalokasikan array baru dan tidak ada yang bisa dirusak.
	opts := make([]retriever.Option, 1, 4)
	opts[0] = retriever.WithTopK(60)

	// tetangga berbagi array yang sama, tapi panjangnya menjangkau slot yang
	// akan ditulisi kalau opsi ditambahkan di tempat.
	tetangga := opts[:3]

	if _, err := r.Retrieve(context.Background(), "pasal 1 membahas apa", opts...); err != nil {
		t.Fatalf("Retrieve() gagal: %v", err)
	}

	if len(opts) != 1 {
		t.Fatalf("panjang opsi pemanggil berubah menjadi %d, mau tetap 1", len(opts))
	}

	// Opsi pemanggil masih harus berarti topK 60 kalau dipakai lagi.
	acuan := 0
	if got := *retriever.GetCommonOptions(&retriever.Options{TopK: &acuan}, opts...).TopK; got != 60 {
		t.Errorf("opsi pemanggil sekarang berarti topK %d, mau tetap 60", got)
	}

	// Slot di luar panjang opts harus masih kosong. Option kosong tidak
	// mengubah apa pun, jadi nilai acuan harus bertahan.
	for i := 1; i < len(tetangga); i++ {
		acuan := 999
		got := *retriever.GetCommonOptions(&retriever.Options{TopK: &acuan}, tetangga[i]).TopK
		if got != 999 {
			t.Errorf("slot %d di luar panjang opts ikut tertulis: topK menjadi %d, mau tetap 999", i, got)
		}
	}
}

// idDokumen mengembalikan ID tiap dokumen, untuk pembanding yang mudah dibaca.
func idDokumen(docs []*schema.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	return out
}

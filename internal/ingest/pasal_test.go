package ingest

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"rag-eino/internal/config"
)

// konfigurasiUji mengembalikan konfigurasi pemotongan tanpa membaca .env,
// supaya test ini tidak bergantung pada keadaan mesin.
func konfigurasiUji() *config.Config {
	return &config.Config{
		ChunkStrategy: "pasal",
		ChunkSize:     500,
		ChunkOverlap:  100,
	}
}

// Pola khusus test. Berbeda dengan reNomorAyat/reNomorButir di pasal.go — yang
// dipakai untuk membaca satu unit yang sudah dipisah, sehingga ^ cukup di awal
// teks — pola di sini dipakai untuk memeriksa chunk yang isinya banyak baris.
// Tanpa (?m), ^ hanya cocok di awal chunk dan semua butir dianggap hilang.
var (
	reUjiAyat  = regexp.MustCompile(`(?m)^- \((\d+)\)`)
	reUjiButir = regexp.MustCompile(`(?m)^- (\d+)\\\.`)
)

func potongUji(t *testing.T, teks string) []*schema.Document {
	t.Helper()

	chunks, err := potongPerPasal(context.Background(), konfigurasiUji(), []*schema.Document{{Content: teks}})
	if err != nil {
		t.Fatalf("potongPerPasal() gagal: %v", err)
	}
	return chunks
}

// dokSederhana adalah dokumen kecil dengan struktur lengkap: pembukaan, dua
// BAB, tiga pasal, dan lampiran.
const dokSederhana = `MENIMBANG

bahwa penyusunan tata naskah dinas perlu diatur.

MEMUTUSKAN

BAB I

KETENTUAN UMUM

Pasal 1

Dalam Peraturan Bupati ini yang dimaksud dengan:

- 1\. Daerah adalah Kabupaten Blitar.
- 2\. Bupati adalah Bupati Blitar.

BAB II

MAKSUD DAN TUJUAN

Pasal 2

Peraturan Bupati ini disusun dengan maksud untuk menjadi acuan bagi Pemerintah Daerah dalam menyusun Naskah Dinas.

Pasal 3

Peraturan Bupati ini disusun dengan tujuan untuk mewujudkan tertib administrasi penyelenggaraan Naskah Dinas.

LAMPIRAN PERATURAN BUPATI

- I. Contoh format kop surat
`

func TestBagiSegmenMengenaliStruktur(t *testing.T) {
	bagian := bagiSegmen(dokSederhana)
	if bagian == nil {
		t.Fatal("bagiSegmen() mengembalikan nil, mau daftar segmen")
	}

	var ringkas []string
	for _, s := range bagian {
		ringkas = append(ringkas, fmt.Sprintf("%s/pasal=%s/bab=%s", s.bagian, s.pasal, s.bab))
	}

	mau := []string{
		"pembukaan/pasal=/bab=",
		"batang_tubuh/pasal=1/bab=I",
		"batang_tubuh/pasal=2/bab=II",
		"batang_tubuh/pasal=3/bab=II",
		"lampiran/pasal=/bab=",
	}

	if len(ringkas) != len(mau) {
		t.Fatalf("bagiSegmen() menghasilkan %d segmen (%v), mau %d", len(ringkas), ringkas, len(mau))
	}
	for i := range mau {
		if ringkas[i] != mau[i] {
			t.Errorf("segmen %d = %q, mau %q", i, ringkas[i], mau[i])
		}
	}
}

// TestJudulBabMasukMetadata memastikan judul BAB ikut tersimpan.
//
// Judul BAB adalah konteks yang tidak ada di dalam isi pasal mana pun, jadi
// tanpa ini ia hilang sama sekali dari hasil retrieval.
func TestJudulBabMasukMetadata(t *testing.T) {
	chunks := potongUji(t, dokSederhana)

	var ketemu bool
	for _, c := range chunks {
		if c.MetaData[metaPasal] != "2" {
			continue
		}
		ketemu = true
		if got := c.MetaData[metaBAB]; got != "II" {
			t.Errorf("bab = %v, mau \"II\"", got)
		}
		if got := c.MetaData[metaBABJudul]; got != "MAKSUD DAN TUJUAN" {
			t.Errorf("bab_judul = %v, mau \"MAKSUD DAN TUJUAN\"", got)
		}
	}
	if !ketemu {
		t.Fatal("tidak ada chunk untuk Pasal 2")
	}
}

// TestPasalPendekDisimpanUtuh memastikan pasal yang muat tidak diubah sama
// sekali — termasuk tidak diberi header buatan.
func TestPasalPendekDisimpanUtuh(t *testing.T) {
	chunks := potongUji(t, dokSederhana)

	var pasal2 []*schema.Document
	for _, c := range chunks {
		if c.MetaData[metaPasal] == "2" {
			pasal2 = append(pasal2, c)
		}
	}

	if len(pasal2) != 1 {
		t.Fatalf("Pasal 2 menghasilkan %d chunk, mau 1", len(pasal2))
	}
	if !strings.HasPrefix(pasal2[0].Content, "Pasal 2\n") {
		t.Errorf("isi Pasal 2 tidak diawali baris penandanya:\n%s", pasal2[0].Content)
	}
	if pasal2[0].MetaData[metaAyat] != "" {
		t.Errorf("ayat = %v, mau kosong untuk pasal yang tidak dipecah", pasal2[0].MetaData[metaAyat])
	}
}

// dokPasalPanjangBerayat membuat satu pasal berayat yang jauh melebihi
// ChunkSize, supaya jalur pemecahan benar-benar terpakai.
func dokPasalPanjangBerayat() string {
	var b strings.Builder
	b.WriteString("BAB I\n\nKETENTUAN UMUM\n\nPasal 1\n\n")
	fmt.Fprintf(&b, "- (1) Ketentuan umum ini meliputi hal-hal berikut: %s\n",
		strings.Repeat("lorem ipsum dolor sit amet ", 5))
	for i := 2; i <= 6; i++ {
		fmt.Fprintf(&b, "- (%d) Ketentuan lanjutan nomor %d yang mengatur hal teknis: %s\n",
			i, i, strings.Repeat("consectetur adipiscing elit sed do ", 5))
	}
	return b.String()
}

// TestPasalPanjangDipecahDenganHeaderRujukan adalah test inti dari perbaikan
// ini.
//
// Sebelum perbaikan, potongan kedua dan seterusnya dari sebuah pasal tidak lagi
// memuat tulisan "Pasal N", sehingga tidak bisa ditemukan lewat rujukan pasal.
// Test ini memastikan setiap potongan membawa rujukannya.
func TestPasalPanjangDipecahDenganHeaderRujukan(t *testing.T) {
	chunks := potongUji(t, dokPasalPanjangBerayat())

	if len(chunks) < 2 {
		t.Fatalf("pasal panjang menghasilkan %d chunk, mau lebih dari satu", len(chunks))
	}

	adaAyat := map[string]bool{}
	jumlah := 0
	for i, c := range chunks {
		// Dokumen uji ini diawali "BAB I", jadi segmen pertama adalah pembukaan
		// yang bukan bagian dari Pasal 1.
		if c.MetaData[metaPasal] != "1" {
			continue
		}
		jumlah++

		if !strings.HasPrefix(c.Content, "Pasal 1 ") {
			t.Errorf("chunk %d tidak diawali rujukan pasal:\n%s", i, potongAwal(c.Content, 120))
		}
		if c.MetaData[metaAyat] == "" {
			t.Errorf("chunk %d: ayat kosong, mau rentang ayat", i)
		}
		if c.MetaData[metaBagian] != bagianBatangTubuh {
			t.Errorf("chunk %d: bagian = %v, mau %q", i, c.MetaData[metaBagian], bagianBatangTubuh)
		}
		for _, m := range reUjiAyat.FindAllStringSubmatch(c.Content, -1) {
			adaAyat[m[1]] = true
		}
	}

	if jumlah < 2 {
		t.Fatalf("Pasal 1 menghasilkan %d chunk, mau lebih dari satu", jumlah)
	}

	// Memecah pasal tidak boleh menghilangkan ayat: keenamnya harus tetap ada.
	for i := 1; i <= 6; i++ {
		if !adaAyat[fmt.Sprint(i)] {
			t.Errorf("ayat (%d) tidak ditemukan di potongan mana pun", i)
		}
	}
}

// dokPasalPanjangTanpaAyat meniru Pasal 1 pada dokumen sungguhan: daftar
// definisi bernomor, tanpa satu pun ayat berbentuk "(1)".
func dokPasalPanjangTanpaAyat() string {
	var b strings.Builder
	b.WriteString("BAB I\n\nKETENTUAN UMUM\n\nPasal 1\n\n")
	b.WriteString("Dalam Peraturan Bupati ini yang dimaksud dengan:\n\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "- %d\\. Istilah nomor %d adalah sesuatu yang perlu dijelaskan panjang lebar agar melebihi batas chunk. %s\n",
			i, i, strings.Repeat("keterangan tambahan ", 4))
	}
	return b.String()
}

// TestPasalPanjangTanpaAyatTetapDiberiHeader memastikan jalur butir bernomor
// juga memberi header.
//
// Ini bentuk yang paling mirip kasus asli: Pasal 1 dokumen sungguhan memakai
// daftar definisi bernomor, bukan ayat.
func TestPasalPanjangTanpaAyatTetapDiberiHeader(t *testing.T) {
	chunks := potongUji(t, dokPasalPanjangTanpaAyat())

	jumlah := 0
	for i, c := range chunks {
		// Segmen pertama adalah "BAB I / KETENTUAN UMUM" (pembukaan).
		if c.MetaData[metaPasal] != "1" {
			continue
		}
		jumlah++

		if !strings.HasPrefix(c.Content, "Pasal 1 ") {
			t.Errorf("chunk %d tidak diawali rujukan pasal:\n%s", i, potongAwal(c.Content, 120))
		}
	}

	if jumlah < 2 {
		t.Fatalf("Pasal 1 menghasilkan %d chunk, mau lebih dari satu", jumlah)
	}
}

// TestSemuaDefinisiPasal1Tercakup memastikan pemecahan tidak menghilangkan
// isi: seluruh nomor butir harus muncul di antara potongan-potongan Pasal 1.
func TestSemuaDefinisiPasal1Tercakup(t *testing.T) {
	chunks := potongUji(t, dokPasalPanjangTanpaAyat())

	ada := map[string]bool{}
	for _, c := range chunks {
		if c.MetaData[metaPasal] != "1" {
			continue
		}
		for _, m := range reUjiButir.FindAllStringSubmatch(c.Content, -1) {
			ada[m[1]] = true
		}
	}

	for i := 1; i <= 12; i++ {
		if !ada[fmt.Sprint(i)] {
			t.Errorf("butir %d tidak ditemukan di potongan mana pun", i)
		}
	}
}

// TestPenandaPasalMundurDitolak menjaga penjagaan terhadap penanda palsu.
//
// Konversi PDF bisa melempar potongan rujukan ke baris sendiri, sehingga
// muncul baris "Pasal N" di tengah pasal lain. Di dokumen sungguhan, tepat di
// tengah Pasal 89 ada baris "Pasal 8" dari tabel yang rusak; tanpa penjagaan
// ini ia menjadi chunk berlabel pasal=8 dengan isi kata acak, menutupi Pasal 8
// yang asli.
//
// Nomor pasal di batang tubuh selalu naik, jadi penanda yang nomornya tidak
// lebih besar dari pasal terakhir ditolak dan teksnya ikut ke pasal sebelumnya.
func TestPenandaPasalMundurDitolak(t *testing.T) {
	const dok = `BAB I

Pasal 1

isi satu

Pasal 2

isi dua

Pasal 1

sisa tabel yang rusak

Pasal 3

isi tiga
`

	bagian := bagiSegmen(dok)
	if bagian == nil {
		t.Fatal("bagiSegmen() mengembalikan nil")
	}

	var nomor, isiPasal2 []string
	for _, s := range bagian {
		if s.pasal != "" {
			nomor = append(nomor, s.pasal)
		}
		if s.pasal == "2" {
			isiPasal2 = append(isiPasal2, s.isi)
		}
	}

	mau := []string{"1", "2", "3"}
	if strings.Join(nomor, ",") != strings.Join(mau, ",") {
		t.Errorf("nomor pasal = %v, mau %v", nomor, mau)
	}

	// Isi penanda palsu tidak boleh hilang; ia menempel ke pasal sebelumnya.
	// Yang penting nomor pasalnya tidak menipu, bukan teksnya dibuang.
	if len(isiPasal2) != 1 || !strings.Contains(isiPasal2[0], "sisa tabel yang rusak") {
		t.Errorf("teks penanda palsu tidak ikut ke Pasal 2:\n%v", isiPasal2)
	}
}

// TestPenandaPasalSetelahLampiranIkutLampiran memastikan penanda pasal yang
// muncul setelah awal lampiran tidak dicap sebagai batang tubuh.
//
// Lampiran boleh punya skema penomoran sendiri, jadi setelah "LAMPIRAN" semua
// pasal — apa pun nomornya — tetap bagian lampiran.
func TestPenandaPasalSetelahLampiranIkutLampiran(t *testing.T) {
	const dok = `Pasal 1

isi batang tubuh

LAMPIRAN PERATURAN BUPATI

Pasal 2

isi lampiran
`

	bagian := bagiSegmen(dok)
	if bagian == nil {
		t.Fatal("bagiSegmen() mengembalikan nil")
	}

	for _, s := range bagian {
		if s.pasal == "1" && s.bagian != bagianBatangTubuh {
			t.Errorf("Pasal 1 bagian = %q, mau %q", s.bagian, bagianBatangTubuh)
		}
		if s.pasal == "2" && s.bagian != bagianLampiran {
			t.Errorf("Pasal 2 bagian = %q, mau %q", s.bagian, bagianLampiran)
		}
	}
}

// TestPenandaPasalMenyambungIsiDikenali menjaga penanda yang tidak berdiri
// sendiri di barisnya.
//
// Konversi PDF kadang menulis "Pasal 7 Jenis Naskah Dinas ... terdiri atas:"
// dalam satu baris. Pola yang menuntut barisnya persis "Pasal N" akan
// melewatkannya, dan akibatnya isi pasal itu menempel ke pasal sebelumnya
// sekaligus nomornya menguap.
func TestPenandaPasalMenyambungIsiDikenali(t *testing.T) {
	const dok = `BAB I

Pasal 1

isi satu

Pasal 7 Jenis Naskah Dinas sebagaimana dimaksud dalam Pasal 4 huruf a terdiri atas:

- a. Naskah Dinas arahan;
- b. Naskah Dinas korespondensi; dan
- c. Naskah Dinas khusus.

Pasal 8

isi delapan
`

	bagian := bagiSegmen(dok)
	if bagian == nil {
		t.Fatal("bagiSegmen() mengembalikan nil")
	}

	var nomor, isiPasal7 []string
	for _, s := range bagian {
		if s.pasal != "" {
			nomor = append(nomor, s.pasal)
		}
		if s.pasal == "7" {
			isiPasal7 = append(isiPasal7, s.isi)
		}
	}

	if strings.Join(nomor, ",") != "1,7,8" {
		t.Errorf("nomor pasal = %v, mau [1 7 8]", nomor)
	}

	if len(isiPasal7) != 1 {
		t.Fatalf("Pasal 7 menghasilkan %d segmen, mau 1", len(isiPasal7))
	}

	// Kalimat pengantar pasalnya harus ikut. Membuang seluruh baris penanda
	// akan menghilangkan "Jenis Naskah Dinas ... terdiri atas:" — bagian yang
	// justru menjelaskan isi daftarnya.
	for _, petikan := range []string{"Jenis Naskah Dinas", "terdiri atas", "Naskah Dinas arahan"} {
		if !strings.Contains(isiPasal7[0], petikan) {
			t.Errorf("segmen Pasal 7 tidak memuat %q:\n%s", petikan, potongAwal(isiPasal7[0], 200))
		}
	}
}

// TestPasalMenyambungYangPanjangTetapSimpanPengantarnya menjaga jalur
// pemecahan, bukan hanya jalur "pasal muat utuh".
//
// Pasal yang muat disimpan apa adanya, sehingga baris penandanya tidak pernah
// disentuh. Jalur pemecahanlah yang memanggil buangJudulPasal — dan di situ
// pola yang terlalu longgar akan membuang seluruh baris penanda beserta
// kalimat pengantar pasalnya.
func TestPasalMenyambungYangPanjangTetapSimpanPengantarnya(t *testing.T) {
	var b strings.Builder
	b.WriteString("BAB I\n\nPasal 1\n\nisi satu\n\n")
	b.WriteString("Pasal 7 Jenis Naskah Dinas sebagaimana dimaksud dalam Pasal 4 huruf a terdiri atas:\n\n")
	for _, butir := range []string{
		"a. Naskah Dinas arahan yang dipakai untuk mengarahkan pelaksanaan tugas pemerintahan",
		"b. Naskah Dinas korespondensi yang dipakai untuk berkomunikasi antar instansi",
		"c. Naskah Dinas khusus yang dipakai untuk keperluan tertentu di luar keduanya",
	} {
		fmt.Fprintf(&b, "- %s. %s\n\n", butir, strings.Repeat("keterangan tambahan ", 4))
	}
	b.WriteString("Pasal 8\n\nisi delapan\n")

	chunks := potongUji(t, b.String())

	var ada bool
	var gabung strings.Builder
	for _, c := range chunks {
		if c.MetaData[metaPasal] != "7" {
			continue
		}
		ada = true
		gabung.WriteString(c.Content)
		gabung.WriteString("\n")
	}

	if !ada {
		t.Fatal("tidak ada chunk untuk Pasal 7")
	}
	if len(chunks) < 2 {
		t.Fatalf("dokumen uji hanya menghasilkan %d chunk, Pasal 7-nya tidak terpecah", len(chunks))
	}

	for _, petikan := range []string{"Jenis Naskah Dinas", "terdiri atas:", "Naskah Dinas arahan"} {
		if !strings.Contains(gabung.String(), petikan) {
			t.Errorf("kalimat pengantar Pasal 7 hilang — %q tidak ada di potongan mana pun", petikan)
		}
	}
}

// TestPasalDenganPenandaMenyambungAdaDiDokumenSungguhan memastikan pasal-pasal
// yang penandanya menyambung dengan isinya benar-benar ada di dokumen asli.
//
// Lima pasal di dokumen ini ditulis seperti itu — 7, 9, 27, 76, dan 119 —
// sehingga sebelumnya semuanya tidak dikenali. Yang paling merugikan adalah
// Pasal 7: isinya pengelompokan naskah dinas tingkat atas, sehingga pertanyaan
// tentang hal itu tidak bisa dijawab.
func TestPasalDenganPenandaMenyambungAdaDiDokumenSungguhan(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	// Seluruh potongan tiap pasal digabung: pasal yang dipecah menaruh isinya
	// di beberapa potongan, dan yang dicari bisa ada di potongan mana pun.
	var ada = map[string]strings.Builder{}
	for _, c := range chunks {
		if c.MetaData[metaBagian] != bagianBatangTubuh {
			continue
		}
		pasal, _ := c.MetaData[metaPasal].(string)
		if pasal == "" {
			continue
		}
		b := ada[pasal]
		b.WriteString(c.Content)
		b.WriteString("\n")
		ada[pasal] = b
	}

	for nomor, petikan := range map[string]string{
		"7":   "Naskah Dinas arahan;",
		"9":   "Naskah Dinas pengaturan",
		"27":  "Naskah Dinas khusus terdiri dari",
		"76":  "Dalam pembuatan Naskah Dinas",
		"119": "Ralat Naskah Dinas",
	} {
		b, ketemu := ada[nomor]
		if !ketemu {
			t.Errorf("Pasal %s tidak ada di antara potongan batang tubuh", nomor)
			continue
		}
		if isi := b.String(); !strings.Contains(isi, petikan) {
			t.Errorf("Pasal %s tidak memuat %q:\n%s", nomor, petikan, potongAwal(isi, 200))
		}
	}
}

// TestDokumenTanpaPenandaPasalJatuhKeRecursive memastikan dokumen non-peraturan
// tetap bisa di-ingest.
//
// Kasus nyatanya PDF, yang dibaca per halaman sehingga penanda pasal bisa
// terpisah dari isinya. Menghasilkan error di situ akan membuat penggantian
// DOC_PATH ke berkas PDF gagal total.
func TestDokumenTanpaPenandaPasalJatuhKeRecursive(t *testing.T) {
	teks := strings.Repeat("Ini dokumen biasa tanpa struktur pasal sama sekali. ", 20)

	chunks := potongUji(t, teks)

	if len(chunks) == 0 {
		t.Fatal("tidak ada chunk yang dihasilkan")
	}
	for i, c := range chunks {
		if c.MetaData[metaBagian] != bagianTanpaPasal {
			t.Errorf("chunk %d: bagian = %v, mau %q", i, c.MetaData[metaBagian], bagianTanpaPasal)
		}
	}
}

// TestMetadataSemuaBertipeString menjaga risiko yang paling tidak terlihat.
//
// Indexer Qdrant memanggil qdrant.NewValueMap, yang PANIC — bukan
// mengembalikan error — untuk tipe yang tidak dikenal. Metadata dengan tipe
// liar akan menjatuhkan proses ingest, bukan melaporkan kesalahan.
func TestMetadataSemuaBertipeString(t *testing.T) {
	for nama, dok := range map[string]string{
		"dokumen sederhana":   dokSederhana,
		"pasal panjang":       dokPasalPanjangBerayat(),
		"tanpa penanda pasal": strings.Repeat("Dokumen biasa. ", 30),
	} {
		t.Run(nama, func(t *testing.T) {
			for i, c := range potongUji(t, dok) {
				for k, v := range c.MetaData {
					if _, ok := v.(string); !ok {
						t.Errorf("chunk %d: metadata %q bertipe %T, mau string", i, k, v)
					}
				}
			}
		})
	}
}

// potongAwal memotong teks untuk pesan error supaya keluarannya terbaca.
func potongAwal(teks string, n int) string {
	teks = strings.Join(strings.Fields(teks), " ")
	if len(teks) <= n {
		return teks
	}
	return teks[:n] + "..."
}

// --- Test terhadap dokumen sungguhan -------------------------------------

// dokSungguhan membaca dokumen sumber dari konfigurasi. Test dilewati kalau
// berkasnya belum ada, supaya `go test ./...` tetap hijau di clone baru.
func dokSungguhan(t *testing.T) ([]*schema.Document, *config.Config) {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("konfigurasi tidak terbaca: %v", err)
	}
	if _, err := os.Stat(cfg.DocPath); err != nil {
		t.Skipf("dokumen %s belum ada", cfg.DocPath)
	}

	docs, err := loadDoc(context.Background(), cfg)
	if err != nil {
		t.Fatalf("membaca dokumen: %v", err)
	}
	return docs, cfg
}

// TestSetiapChunkBatangTubuhDiawaliPasal adalah pengunci bug yang dilaporkan.
//
// Sebelum perbaikan, 7 dari 8 chunk Pasal 1 tidak memuat tulisan "Pasal 1"
// sama sekali, sehingga pertanyaan "pasal 1 membahas apa" tidak bisa dijawab.
// Test ini memastikan tidak ada satu pun potongan batang tubuh yang kehilangan
// rujukan pasalnya.
func TestSetiapChunkBatangTubuhDiawaliPasal(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	batang := 0
	for i, c := range chunks {
		if c.MetaData[metaBagian] != bagianBatangTubuh {
			continue
		}
		batang++

		if !strings.HasPrefix(c.Content, "Pasal ") {
			t.Errorf("chunk %d tidak diawali rujukan pasal (pasal=%v):\n%s",
				i, c.MetaData[metaPasal], potongAwal(c.Content, 150))
		}
		if c.MetaData[metaPasal] == "" {
			t.Errorf("chunk %d tidak punya nomor pasal di metadatanya", i)
		}
	}

	if batang == 0 {
		t.Fatal("tidak ada chunk batang tubuh; penanda pasal sepertinya tidak dikenali")
	}
	t.Logf("chunk batang tubuh: %d dari %d chunk", batang, len(chunks))
}

// TestDefinisiPasal1Lengkap memastikan seluruh 17 definisi Pasal 1 benar-benar
// tersimpan, bukan hanya tiga yang kebetulan masuk chunk pertama seperti
// sebelum perbaikan.
func TestDefinisiPasal1Lengkap(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	ada := map[string]bool{}
	jumlahChunk := 0
	for _, c := range chunks {
		if c.MetaData[metaPasal] != "1" || c.MetaData[metaBagian] != bagianBatangTubuh {
			continue
		}
		jumlahChunk++
		for _, m := range reUjiButir.FindAllStringSubmatch(c.Content, -1) {
			ada[m[1]] = true
		}
	}

	if jumlahChunk == 0 {
		t.Fatal("tidak ada chunk untuk Pasal 1")
	}

	// Pasal 1 dokumen ini berisi 17 definisi bernomor.
	const jumlahDefinisi = 17
	for i := 1; i <= jumlahDefinisi; i++ {
		if !ada[fmt.Sprint(i)] {
			t.Errorf("definisi %d tidak ditemukan di antara %d chunk Pasal 1", i, jumlahChunk)
		}
	}

	t.Logf("Pasal 1 terpecah menjadi %d chunk, semuanya membawa rujukan pasal", jumlahChunk)
}

// TestTidakAdaChunkMelebihiBatas menjaga agar pemecahan benar-benar terjadi.
//
// Batasnya ChunkSize ditambah kelonggaran untuk header dan untuk satu unit yang
// sendirian sudah melebihi batas (kasus yang tidak bisa dihindari).
func TestTidakAdaChunkMelebihiBatas(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	// Kelonggaran lebar: satu butir/ayat yang sendirian panjang, atau sisa
	// teks yang tidak punya batas internal, bisa melewati ChunkSize.
	batas := cfg.ChunkSize * 4
	var terpanjang int

	for _, c := range chunks {
		if len(c.Content) > terpanjang {
			terpanjang = len(c.Content)
		}
		if len(c.Content) > batas {
			t.Errorf("ada chunk sepanjang %d karakter (batas %d):\n%s",
				len(c.Content), batas, potongAwal(c.Content, 150))
		}
	}

	t.Logf("chunk terpanjang: %d karakter (ChunkSize=%d, batas uji=%d)",
		terpanjang, cfg.ChunkSize, batas)
}

// TestJumlahChunkWajar menjaga agar perubahan pola tidak diam-diam memotong
// dokumen menjadi beberapa bagian saja, atau sebaliknya memecahnya berlebihan.
func TestJumlahChunkWajar(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	// Dokumen ini punya 149 pasal. Rentangnya lebar dengan sengaja: yang dijaga
	// adalah urutan besarnya, bukan angka tepatnya.
	const min, maks = 150, 500
	if len(chunks) < min || len(chunks) > maks {
		t.Errorf("jumlah chunk = %d, mau antara %d dan %d", len(chunks), min, maks)
	}

	t.Logf("total chunk: %d", len(chunks))
}

// TestNomorPasalBertambahMenurun memastikan urutan pasal terjaga dan tidak ada
// pasal yang hilang di tengah.
//
// Regex yang salah bisa membuat sebagian penanda tidak dikenali, dan gejalanya
// halus: dokumennya tetap terpotong, hanya beberapa pasal yang menguap.
func TestNomorPasalBertambahMenurun(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	pola := regexp.MustCompile(`^Pasal (\d+)`)
	terakhir := 0
	for i, c := range chunks {
		if c.MetaData[metaBagian] != bagianBatangTubuh {
			continue
		}
		m := pola.FindStringSubmatch(c.Content)
		if m == nil {
			continue
		}
		n := 0
		fmt.Sscanf(m[1], "%d", &n)

		if n < terakhir {
			t.Fatalf("chunk %d menyebut Pasal %d setelah Pasal %d; urutannya kacau", i, n, terakhir)
		}
		terakhir = n
	}

	if terakhir < 149 {
		t.Errorf("pasal terakhir yang ditemukan = %d, mau minimal 149", terakhir)
	}
}

// TestPasal89Dan90TidakTeracakDiDokumenSungguhan mengunci perbaikan manual
// pada Markdown sumber.
//
// Bagian amplop di PDF ditulis rata kiri-kanan (justify) sehingga xberg membaca
// kata-katanya berselang-seling antarbaris. Sebelum ditambal, Pasal 89 dan 90
// keluar sebagai rangkaian kata acak — "b. cokelat untuk sebagaimana dimaksud
// naskah dinas sebagaimana dimaksud amplop dalam" — dan memunculkan penanda
// "Pasal 8" palsu di tengahnya, yang sempat membuat potongan berlabel pasal 8
// berisi kata-kata tak berhubungan.
//
// Teks penggantinya diambil dari `pdftotext -layout` halaman 34-36, yang urutan
// bacanya benar. Lihat README, bagian "Mengganti dokumen".
//
// Test ini akan gagal kalau Markdown dikonversi ulang dengan xberg tanpa
// menambal ulang — dan itu memang tujuannya.
func TestPasal89Dan90TidakTeracakDiDokumenSungguhan(t *testing.T) {
	docs, cfg := dokSungguhan(t)

	chunks, err := splitDocs(context.Background(), cfg, docs)
	if err != nil {
		t.Fatalf("splitDocs() gagal: %v", err)
	}

	var ada = map[string]strings.Builder{}
	for _, c := range chunks {
		if c.MetaData[metaBagian] != bagianBatangTubuh {
			continue
		}
		pasal, _ := c.MetaData[metaPasal].(string)
		if pasal == "" {
			continue
		}
		b := ada[pasal]
		b.WriteString(c.Content)
		b.WriteString("\n")
		ada[pasal] = b
	}

	// Petikan yang hanya muncul kalau urutan katanya benar.
	for nomor, petikan := range map[string]string{
		"89": "menggunakan kertas casing dengan warna",
		"90": "harus dicantumkan alamat pengirim dan alamat tujuan",
	} {
		b, ketemu := ada[nomor]
		if !ketemu {
			t.Errorf("Pasal %s tidak ada di antara potongan batang tubuh", nomor)
			continue
		}
		if isi := b.String(); !strings.Contains(isi, petikan) {
			t.Errorf("Pasal %s tidak memuat %q; tambalan Markdown hilang?\n%s",
				nomor, petikan, potongAwal(isi, 300))
		}
	}

	// Sisa kata yang hanya ada di versi teracak. Salah satu saja muncul berarti
	// Markdown sudah dikonversi ulang tanpa ditambal.
	teracak := []string{
		"sebagaimana dimaksud naskah dinas sebagaimana dimaksud",
		"casing dengan dinas jabatan",
		"Pasal 8 naskah",
	}
	for _, c := range chunks {
		isi := c.Content
		for _, pecahan := range teracak {
			if strings.Contains(isi, pecahan) {
				t.Errorf("chunk berpasal %v masih memuat sisa teks teracak %q:\n%s",
					c.MetaData[metaPasal], pecahan, potongAwal(isi, 200))
			}
		}
	}
}

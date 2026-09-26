package ingest

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"

	"rag-eino/internal/config"
)

// chunkUji membuat Document dari isi mentah, tanpa metadata.
func chunkUji(isi ...string) []*schema.Document {
	docs := make([]*schema.Document, 0, len(isi))
	for _, s := range isi {
		docs = append(docs, &schema.Document{Content: s})
	}
	return docs
}

// isiChunk mengembalikan isi tiap chunk, untuk pembanding yang mudah dibaca.
func isiChunk(docs []*schema.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.Content)
	}
	return out
}

// teksPanjang menghasilkan teks acak-ulang yang cukup panjang dan berhuruf,
// supaya lolos saringan dan yang diuji benar-benar hanya perilaku yang dimau.
func teksPanjang(awalan string) string {
	return awalan + " " + strings.Repeat("ketentuan naskah dinas daerah ", 6)
}

// TestSaringDegeneratMembuangChunkPendek mengunci alasan saringan ini ada.
//
// Sebelum saringan ini, salah satu chunk panjangnya hanya beberapa karakter,
// dan ia muncul di peringkat 2 hasil pencarian untuk "pasal 1" — di atas pasal
// yang sungguhan. Chunk seperti itu menghasilkan vektor yang nyaris degenerate
// dan bisa berskor tinggi terhadap pertanyaan apa pun.
func TestSaringDegeneratMembuangChunkPendek(t *testing.T) {
	chunks := chunkUji(
		teksPanjang("Pasal 1"),
		"3 karakter",
		teksPanjang("Pasal 2"),
		teksPanjang("Pasal 3"),
	)

	hasil := saringDegenerat(chunks)

	mau := []string{teksPanjang("Pasal 1"), teksPanjang("Pasal 2"), teksPanjang("Pasal 3")}
	if got := isiChunk(hasil); strings.Join(got, "|") != strings.Join(mau, "|") {
		t.Errorf("hasil saringan = %v, mau %v", got, mau)
	}
}

// TestSaringDegeneratMembuangNyarisTanpaHuruf menangkap bentuk yang paling
// berbahaya: chunk yang panjangnya cukup, tapi isinya hampir seluruhnya titik.
//
// Ini bentuk nyata template kop surat di dokumen sumber — deretan titik-titik
// sebagai tempat isian. Panjangnya melewati ambang karakter, jadi hanya
// hitungan karakter alfanumerik yang bisa menangkapnya.
func TestSaringDegeneratMembuangNyarisTanpaHuruf(t *testing.T) {
	titikTitik := strings.Repeat(".", 200)

	if len(titikTitik) < minChunkChars {
		t.Fatalf("teks uji hanya %d karakter, tidak melewati ambang %d — testnya tidak menguji apa pun",
			len(titikTitik), minChunkChars)
	}

	chunks := chunkUji(titikTitik, teksPanjang("Pasal 1"))

	hasil := saringDegenerat(chunks)

	if len(hasil) != 1 || hasil[0].Content != teksPanjang("Pasal 1") {
		t.Errorf("hasil saringan = %v, mau hanya chunk yang berisi huruf", isiChunk(hasil))
	}
}

// TestSaringDegeneratMembuangKembar memastikan isi kembar tidak di-embed dua
// kali.
//
// Selain boros kuota, isi kembar toh berbagi satu ID di assignChunkIDs —
// jadi menyimpannya dua kali memang tidak mungkin.
func TestSaringDegeneratMembuangKembar(t *testing.T) {
	sama := teksPanjang("Pasal 7")
	chunks := chunkUji(teksPanjang("Pasal 1"), sama, sama, teksPanjang("Pasal 2"))

	hasil := saringDegenerat(chunks)

	mau := []string{teksPanjang("Pasal 1"), sama, teksPanjang("Pasal 2")}
	if got := isiChunk(hasil); strings.Join(got, "|") != strings.Join(mau, "|") {
		t.Errorf("hasil saringan = %v, mau %v", got, mau)
	}
}

// TestSaringDegeneratMembuangKembarDenganSpasiBerbeda memastikan pembanding
// kembar memakai isi yang sudah di-trim.
//
// Chunk dari splitter berbasis ukuran bisa berbeda hanya di spasi di ujung,
// dan isi seperti itu praktis sama saja.
func TestSaringDegeneratMembuangKembarDenganSpasiBerbeda(t *testing.T) {
	teks := teksPanjang("Pasal 7")
	chunks := chunkUji(teks, "\n  "+teks+"\n\n")

	hasil := saringDegenerat(chunks)

	if len(hasil) != 1 {
		t.Errorf("hasil saringan = %d chunk, mau 1 (isinya sama setelah di-trim)", len(hasil))
	}
}

// TestSaringDegeneratMenjagaUrutan memastikan saringan tidak mengacak urutan.
//
// Urutan chunk menentukan urutan rujukan yang dibaca model, jadi mengacaknya
// diam-diam akan mengubah jawaban.
func TestSaringDegeneratMenjagaUrutan(t *testing.T) {
	chunks := chunkUji(
		teksPanjang("Pasal 1"),
		"pendek",
		teksPanjang("Pasal 2"),
		strings.Repeat(".", 150),
		teksPanjang("Pasal 3"),
	)

	hasil := saringDegenerat(chunks)

	mau := []string{teksPanjang("Pasal 1"), teksPanjang("Pasal 2"), teksPanjang("Pasal 3")}
	if got := isiChunk(hasil); strings.Join(got, "|") != strings.Join(mau, "|") {
		t.Errorf("urutan hasil = %v, mau %v", got, mau)
	}
}

// TestSaringDegeneratMasukanKosong menjaga kasus batas.
func TestSaringDegeneratMasukanKosong(t *testing.T) {
	if hasil := saringDegenerat(nil); len(hasil) != 0 {
		t.Errorf("saringDegenerat(nil) = %d chunk, mau 0", len(hasil))
	}
}

// TestJumlahAlnum memastikan hitungannya sesuai definisi: huruf dan angka,
// termasuk huruf beraksen dan angka non-ASCII.
func TestJumlahAlnum(t *testing.T) {
	for nama, kasus := range map[string]struct {
		teks string
		mau  int
	}{
		"kosong":              {"", 0},
		"tanda baca saja":     {"...---!!! ,,,", 0},
		"huruf dan spasi":     {"abc def", 6},
		"angka":               {"12345", 5},
		"campur":              {"Pasal 1", 6},
		"huruf beraksen":      {"Pasal èéå", 8},
		"angka arab":          {"Pasal ٢", 6},
		"baris baru dihitung": {"a\nb", 2},
	} {
		t.Run(nama, func(t *testing.T) {
			if got := jumlahAlnum(kasus.teks); got != kasus.mau {
				t.Errorf("jumlahAlnum(%q) = %d, mau %d", kasus.teks, got, kasus.mau)
			}
		})
	}
}

// TestSidikJariMengikutiKonfigurasi memastikan sidik jari berubah ketika salah
// satu parameter pemotongan berubah.
//
// Kalau ia tidak berubah, test idempotensi akan mengira collection lama masih
// cocok, lalu melaporkan bug idempotensi padahal yang terjadi cuma data basi.
func TestSidikJariMengikutiKonfigurasi(t *testing.T) {
	dasar := func() *config.Config {
		return &config.Config{ChunkStrategy: "pasal", ChunkSize: 500, ChunkOverlap: 100}
	}

	acuan := sidikJariPemotongan(dasar())

	for nama, ubah := range map[string]func(*config.Config){
		"strategi": func(c *config.Config) { c.ChunkStrategy = "recursive" },
		"ukuran":   func(c *config.Config) { c.ChunkSize = 800 },
		"overlap":  func(c *config.Config) { c.ChunkOverlap = 0 },
	} {
		t.Run(nama, func(t *testing.T) {
			c := dasar()
			ubah(c)
			if got := sidikJariPemotongan(c); got == acuan {
				t.Errorf("sidik jari tidak berubah setelah %s diubah: %q", nama, got)
			}
		})
	}
}

// TestTandaiSidikJariMenanganiMetadataKosong menjaga chunk yang belum punya
// metadata sama sekali — jalur recursive splitter tidak mengisinya.
//
// Menulis ke map nil akan panic, dan itu akan menjatuhkan seluruh ingest.
func TestTandaiSidikJariMenanganiMetadataKosong(t *testing.T) {
	cfg := &config.Config{ChunkStrategy: "recursive", ChunkSize: 500, ChunkOverlap: 100}
	chunks := []*schema.Document{{Content: teksPanjang("Pasal 1")}}

	tandaiSidikJari(chunks, cfg)

	mau := sidikJariPemotongan(cfg)
	if got := chunks[0].MetaData[metaSidikJari]; got != mau {
		t.Errorf("sidik_jari = %v, mau %q", got, mau)
	}
}

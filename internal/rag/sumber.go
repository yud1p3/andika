package rag

import (
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// Kunci metadata potongan yang dipakai menyusun label sumber. Nilainya ditulis
// oleh internal/ingest; di sini hanya dibaca.
const kunciBagian = "bagian"

// LabelSumber menyusun daftar rujukan dari potongan hasil retrieval.
//
// Urutannya mengikuti peringkat retrieval — yang paling menentukan lebih dulu —
// dan label yang sama hanya muncul sekali. Satu pasal sering terpecah menjadi
// beberapa potongan, dan menampilkan "Pasal 89" tujuh kali tidak menambah
// informasi apa pun bagi pembaca.
//
// Yang dikembalikan hanya teks penanda, bukan dokumennya: lapisan tampilan
// hanya perlu memberi tahu pembaca bagian mana yang dipakai, dan menyerahkan
// potongan utuh ke sana akan mengundang isinya ikut bocor ke halaman.
func LabelSumber(docs []*schema.Document) []string {
	hasil := make([]string, 0, len(docs))
	dilihat := make(map[string]bool, len(docs))

	for _, d := range docs {
		label := labelSumber(d)
		if dilihat[label] {
			continue
		}
		dilihat[label] = true
		hasil = append(hasil, label)
	}
	return hasil
}

// reLabelPasal memisahkan nomor dari label "Pasal N".
var reLabelPasal = regexp.MustCompile(`^Pasal (\d+)$`)

// SaringSumber menyisakan label rujukan yang penandanya benar-benar muncul di
// teks jawaban.
//
// Kenapa perlu. LabelSumber menyusun daftar dari SELURUH potongan hasil
// retrieval, bukan hanya yang dipakai menjawab. Di jalur agen selisihnya paling
// terasa: jejakRetrieval mengumpulkan potongan dari setiap panggilan tool
// selama satu permintaan (lihat tools.go), jadi agen yang mencari dua kali
// dengan TOP_K=10 bisa memunculkan sampai 20 label — walaupun jawabannya hanya
// bersumber dari satu pasal. Terukur pada "Apa saja yang termasuk Naskah Dinas
// arahan": 19 label rujukan, padahal jawabannya memakai Pasal 8 saja.
//
// Aturannya sengaja sederhana: label lolos kalau penandanya disebut di
// jawaban. "Pasal 8" lolos kalau jawaban menyebut "Pasal 8" — bukan "Pasal 89",
// batas kata dijaga karena polanya menangkap deret angkanya secara utuh. Label
// lain — "Lampiran", "Pembukaan", "Dokumen" — dicocokkan sebagai kata biasa.
// Urutan label dipertahankan.
//
// Batasnya disadari: model yang menjawab benar tanpa menyebut pasalnya akan
// kehilangan rujukan. Karena itu kedua jalur diminta menyebut pasal sumbernya —
// lihat systemInstruction di chain.go dan agenInstruction di agent.go. Jawaban
// kosong menghasilkan daftar kosong, bukan seluruh label: tanpa jawaban, tidak
// ada yang bisa disebut "dipakai menjawab".
func SaringSumber(label []string, jawaban string) []string {
	if len(label) == 0 || strings.TrimSpace(jawaban) == "" {
		return nil
	}

	teks := strings.ToLower(jawaban)
	hasil := make([]string, 0, len(label))
	for _, l := range label {
		if disebutDiJawaban(l, teks) {
			hasil = append(hasil, l)
		}
	}
	return hasil
}

// disebutDiJawaban memutuskan apakah satu label muncul di teks jawaban yang
// sudah dinormalkan ke huruf kecil.
//
// reRujukanPasal dipakai ulang, bukan disusun lagi: ia sudah menjadi pola baku
// "pasal N" di paket ini, dan karena menangkap deret angkanya utuh, "Pasal 8"
// tidak akan cocok dengan jawaban yang hanya menyebut "Pasal 89".
func disebutDiJawaban(label, teks string) bool {
	if m := reLabelPasal.FindStringSubmatch(label); m != nil {
		for _, d := range reRujukanPasal.FindAllStringSubmatch(teks, -1) {
			if d[1] == m[1] {
				return true
			}
		}
		return false
	}
	return strings.Contains(teks, strings.ToLower(label))
}

// labelSumber menentukan teks penanda sebuah potongan.
//
// Nomor pasal diutamakan karena itulah yang dicari pembaca peraturan. Potongan
// yang tidak berpasal — pembukaan, Lampiran, atau dokumen non-peraturan — jatuh
// ke nama bagiannya supaya tetap ada rujukan yang bisa ditelusuri.
func labelSumber(doc *schema.Document) string {
	meta := bacaMetadata(doc)

	if pasal := meta[kunciPasal]; pasal != "" {
		return "Pasal " + pasal
	}

	switch meta[kunciBagian] {
	case "lampiran":
		return "Lampiran"
	case "pembukaan":
		return "Pembukaan"
	default:
		return "Dokumen"
	}
}

package rag

import (
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

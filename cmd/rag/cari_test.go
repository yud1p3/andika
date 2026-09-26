package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBarisMetadataMengurutkanKunci mengunci pengurutan kunci metadata.
//
// Urutan map di Go sengaja diacak, jadi tanpa pengurutan keluaran perintah
// "rag cari" berubah-ubah setiap kali dijalankan. Itu membuat dua hasil
// pencarian tidak bisa dibandingkan — padahal membandingkan justru gunanya.
func TestBarisMetadataMengurutkanKunci(t *testing.T) {
	meta := map[string]string{
		"pasal":      "1",
		"bagian":     "batang_tubuh",
		"ayat":       "1-5",
		"bab":        "I",
		"bab_judul":  "KETENTUAN UMUM",
		"sidik_jari": "pasal/500/100",
	}

	mau := "ayat=1-5 bab=I bab_judul=KETENTUAN UMUM bagian=batang_tubuh pasal=1 sidik_jari=pasal/500/100"

	// Diulang beberapa kali: keluaran yang bergantung pada urutan map baru
	// ketahuan kalau ada percobaan yang berbeda.
	for i := 0; i < 20; i++ {
		if got := barisMetadata(meta); got != mau {
			t.Fatalf("percobaan %d: barisMetadata() = %q, mau %q", i, got, mau)
		}
	}
}

func TestBarisMetadataKosong(t *testing.T) {
	if got := barisMetadata(nil); got != "" {
		t.Errorf("barisMetadata(nil) = %q, mau kosong", got)
	}
}

func TestRingkas(t *testing.T) {
	for nama, kasus := range map[string]struct {
		isi  string
		maks int
		mau  string
	}{
		"pendek dibiarkan": {
			isi:  "Pasal 1 membahas ketentuan umum.",
			maks: 200,
			mau:  "Pasal 1 membahas ketentuan umum.",
		},
		"baris baru dan spasi dirapatkan": {
			isi:  "Pasal 1\n\n  ayat (1)\tberbunyi",
			maks: 200,
			mau:  "Pasal 1 ayat (1) berbunyi",
		},
		"terpotong diberi elipsis": {
			isi:  strings.Repeat("ab ", 10),
			maks: 5,
			mau:  "ab ab…",
		},
		"tepat pada batas tidak dipotong": {
			isi:  "abcdef",
			maks: 6,
			mau:  "abcdef",
		},
	} {
		t.Run(nama, func(t *testing.T) {
			if got := ringkas(kasus.isi, kasus.maks); got != kasus.mau {
				t.Errorf("ringkas(%q, %d) = %q, mau %q", kasus.isi, kasus.maks, got, kasus.mau)
			}
		})
	}
}

// TestRingkasMemotongPerRune menjaga agar karakter non-ASCII tidak terbelah.
//
// Memotong string per byte di tengah karakter UTF-8 menghasilkan byte rusak,
// dan dokumen peraturan banyak memuat karakter beraksen.
func TestRingkasMemotongPerRune(t *testing.T) {
	isi := strings.Repeat("é", 20)

	got := ringkas(isi, 5)

	if !utf8.ValidString(got) {
		t.Fatalf("hasilnya bukan UTF-8 yang sah: %q", got)
	}
	if got != "ééééé…" {
		t.Errorf("ringkas() = %q, mau %q", got, "ééééé…")
	}
}

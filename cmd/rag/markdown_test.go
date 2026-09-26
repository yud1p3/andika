package main

import (
	"strings"
	"testing"
)

func TestRenderMarkdownBerGaya(t *testing.T) {
	cases := []struct {
		nama string
		in   string
		want string
	}{
		{"tebal", "**tebal**", ansiBold + "tebal" + ansiReset},
		{"tebal garis bawah", "__tebal__", ansiBold + "tebal" + ansiReset},
		{"miring", "*miring*", ansiItalic + "miring" + ansiReset},
		{"kode", "`kode`", ansiCyan + "kode" + ansiReset},
		{"judul", "## Judul", ansiBold + "Judul" + ansiReset},
		{"judul satu pagar", "# Judul", ansiBold + "Judul" + ansiReset},
		{"butir bintang", "* apel", "• apel"},
		{"butir strip", "- apel", "• apel"},
		{"butir plus", "+ apel", "• apel"},
		{"butir menjorok", "  * apel", "  • apel"},
		{"bernomor titik", "1. apel", ansiBold + "1." + ansiReset + " apel"},
		{"bernomor kurung", "2) apel", ansiBold + "2)" + ansiReset + " apel"},
		{"kutipan", "> catatan", ansiDim + "│ " + ansiReset + "catatan"},
		{"garis pemisah", "---", ansiDim + strings.Repeat("─", markdownWidth) + ansiReset},
		{"baris kosong", "   ", ""},
		{"teks biasa", "apa adanya", "apa adanya"},
		{
			"tebal di dalam butir",
			"* **Tempat:** dingin",
			"• " + ansiBold + "Tempat:" + ansiReset + " dingin",
		},
		{
			"bernomor dengan tebal",
			"1. **Suhu:** 15 ºC",
			ansiBold + "1." + ansiReset + " " + ansiBold + "Suhu:" + ansiReset + " 15 ºC",
		},

		// Kasus yang TIDAK boleh berubah, supaya penanda biasa tidak salah
		// dibaca sebagai format.
		{"bintang tunggal", "2 * 3 = 6", "2 * 3 = 6"},
		{"bintang berspasi", "a * b * c", "a * b * c"},
		{"garis bawah dalam kata", "nama_variabel_ini", "nama_variabel_ini"},
		{"pagar tanpa spasi", "#tag", "#tag"},
		{"strip bukan butir", "-15 ºC", "-15 ºC"},
	}

	for _, c := range cases {
		t.Run(c.nama, func(t *testing.T) {
			if got := renderMarkdown(c.in, true); got != c.want {
				t.Errorf("renderMarkdown(%q) = %q, mau %q", c.in, got, c.want)
			}
		})
	}
}

// TestRenderMarkdownTanpaGaya memastikan keluaran yang dialihkan ke file atau
// pipe tidak disisipi kode ANSI, dan isinya tidak diubah sama sekali.
func TestRenderMarkdownTanpaGaya(t *testing.T) {
	const md = "**tebal** dan *miring* serta `kode`\n* butir"

	if got := renderMarkdown(md, false); got != md {
		t.Errorf("tanpa gaya harus dikembalikan apa adanya, dapat %q", got)
	}
}

// TestRenderMarkdownJawabanNyata memakai bentuk jawaban yang benar-benar
// dihasilkan model, untuk memastikan penandanya bersih di keluaran akhir.
func TestRenderMarkdownJawabanNyata(t *testing.T) {
	const jawaban = "Berdasarkan konteks yang diberikan:\n\n" +
		"1. **Tempat dan Suhu Penyimpanan:**\n" +
		"   * **Cooler Bag (15 ºC):** Tahan hingga 24 jam.\n" +
		"   * **Freezer 2 Pintu (-20 ºC):** Tahan 3 - 6 bulan.\n" +
		"2. **Catatan Penting:**\n" +
		"   * Simpan ASI perah **15-60 ml per wadah**.\n\n" +
		"> Beri label tanggal pada setiap wadah.\n\n" +
		"---\n"

	hasil := renderMarkdown(jawaban, true)

	for _, penanda := range []string{"**", "__", "`"} {
		if strings.Contains(hasil, penanda) {
			t.Errorf("penanda %q masih tersisa di hasil:\n%s", penanda, hasil)
		}
	}

	// Struktur tetap terbaca setelah penanda diubah jadi gaya.
	for _, wajib := range []string{
		ansiBold + "1." + ansiReset,
		"   • " + ansiBold + "Cooler Bag (15 ºC):" + ansiReset,
		ansiDim + "│ " + ansiReset + "Beri label tanggal",
		ansiDim + strings.Repeat("─", markdownWidth) + ansiReset,
	} {
		if !strings.Contains(hasil, wajib) {
			t.Errorf("hasil tidak memuat %q:\n%s", wajib, hasil)
		}
	}
}

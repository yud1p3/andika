// Perender Markdown sederhana untuk keluaran terminal.
//
// Model Gemini menjawab dalam format Markdown (`**tebal**`, daftar berbutir,
// judul). Kalau dicetak apa adanya, penanda itu muncul sebagai karakter mentah
// (`**tebal**`, `*`) sehingga justru terbaca berantakan. File ini mengubahnya
// menjadi gaya ANSI supaya enak dibaca.
//
// Ini sengaja bukan perender Markdown lengkap: hanya penanda yang benar-benar
// muncul di jawaban RAG yang ditangani (tebal, miring, kode, judul, daftar,
// kutipan, garis pemisah). Tabel tidak dirapikan.
package main

import (
	"os"
	"strings"
)

// Kode ANSI untuk menata teks di terminal.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiItalic = "\x1b[3m"
	ansiDim    = "\x1b[2m"
	ansiCyan   = "\x1b[36m"
)

// markdownWidth adalah panjang garis pemisah yang dipakai untuk mengganti
// `---`. Angka tetap, bukan lebar terminal, supaya keluarannya sama di mana pun.
const markdownWidth = 44

// stdoutBergaya melaporkan apakah keluaran boleh memakai kode ANSI.
//
// ANSI dimatikan kalau stdout bukan terminal (misalnya dialihkan ke file atau
// pipe) supaya kode escape tidak mengotori hasil, dan kalau variabel NO_COLOR
// diisi — konvensi umum yang dipakai banyak perkakas CLI.
func stdoutBergaya() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	// Terminal adalah perangkat karakter; file dan pipe bukan.
	return info.Mode()&os.ModeCharDevice != 0
}

// renderMarkdown menata teks Markdown untuk terminal. Kalau berGaya=false,
// teks dikembalikan apa adanya supaya tidak ada penanda yang hilang.
func renderMarkdown(md string, berGaya bool) string {
	if !berGaya {
		return md
	}

	lines := strings.Split(md, "\n")
	for i, ln := range lines {
		lines[i] = renderBaris(ln)
	}
	return strings.Join(lines, "\n")
}

// renderBaris menata satu baris: mengenali dulu penanda tingkat blok (judul,
// daftar, kutipan, garis), lalu menata sisa teksnya secara inline.
func renderBaris(ln string) string {
	isi := strings.TrimSpace(ln)
	if isi == "" {
		return ""
	}

	// Indentasi dipertahankan supaya daftar bertingkat tetap terlihat.
	indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]

	if isGarisPemisah(isi) {
		return indent + ansiDim + strings.Repeat("─", markdownWidth) + ansiReset
	}

	if judul, ok := potongJudul(isi); ok {
		return indent + ansiBold + renderInline(judul) + ansiReset
	}

	if strings.HasPrefix(isi, ">") {
		return indent + ansiDim + "│ " + ansiReset + renderInline(strings.TrimSpace(isi[1:]))
	}

	if teks, ok := potongButir(isi); ok {
		return indent + "• " + renderInline(teks)
	}

	if nomor, teks, ok := potongBernomor(isi); ok {
		return indent + ansiBold + nomor + ansiReset + " " + renderInline(teks)
	}

	return indent + renderInline(isi)
}

// isGarisPemisah mengenali `---`, `***`, atau `___` (minimal tiga).
func isGarisPemisah(s string) bool {
	if len(s) < 3 {
		return false
	}

	c := s[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}

	for i := 0; i < len(s); i++ {
		if s[i] != c && s[i] != ' ' {
			return false
		}
	}
	return true
}

// potongJudul mengenali `# Judul` sampai `###### Judul` dan mengembalikan
// teksnya tanpa tanda pagar.
func potongJudul(s string) (string, bool) {
	i := 0
	for i < len(s) && s[i] == '#' {
		i++
	}

	// CommonMark mensyaratkan spasi setelah tanda pagar; `#tag` bukan judul.
	if i == 0 || i > 6 || i >= len(s) || s[i] != ' ' {
		return "", false
	}
	return strings.TrimSpace(s[i:]), true
}

// potongButir mengenali butir daftar (`* `, `- `, `+ `). Spasi setelah penanda
// wajib, supaya `*miring*` tidak salah dibaca sebagai butir.
func potongButir(s string) (string, bool) {
	if len(s) < 2 {
		return "", false
	}

	switch s[0] {
	case '*', '-', '+':
		if s[1] == ' ' {
			return strings.TrimSpace(s[2:]), true
		}
	}
	return "", false
}

// potongBernomor mengenali butir bernomor (`1. ` atau `1) `) dan mengembalikan
// nomornya beserta teksnya.
func potongBernomor(s string) (string, string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}

	if i == 0 || i+1 >= len(s) {
		return "", "", false
	}
	if s[i] != '.' && s[i] != ')' {
		return "", "", false
	}
	if s[i+1] != ' ' {
		return "", "", false
	}

	return s[:i+1], strings.TrimSpace(s[i+2:]), true
}

// renderInline menata penanda di dalam satu baris: `**tebal**`, `__tebal__`,
// `*miring*`, dan `kode`.
//
// Garis bawah tunggal (`_miring_`) sengaja tidak ditangani supaya teks seperti
// `snake_case` tidak ikut berubah.
func renderInline(s string) string {
	var b strings.Builder

	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], "**"):
			if akhir := strings.Index(s[i+2:], "**"); akhir >= 0 {
				b.WriteString(ansiBold + renderInline(s[i+2:i+2+akhir]) + ansiReset)
				i += 2 + akhir + 2
				continue
			}

		case strings.HasPrefix(s[i:], "__"):
			if akhir := strings.Index(s[i+2:], "__"); akhir >= 0 {
				b.WriteString(ansiBold + renderInline(s[i+2:i+2+akhir]) + ansiReset)
				i += 2 + akhir + 2
				continue
			}

		case s[i] == '`':
			if akhir := strings.IndexByte(s[i+1:], '`'); akhir >= 0 {
				b.WriteString(ansiCyan + s[i+1:i+1+akhir] + ansiReset)
				i += 1 + akhir + 1
				continue
			}

		case s[i] == '*':
			// Mengikuti aturan CommonMark: penekanan tidak boleh diawali atau
			// diakhiri spasi. Tanpa syarat ini, teks biasa seperti
			// `2 * 3 = 6` ikut terbaca sebagai miring. Butuh isi di antara dua
			// bintang juga, supaya tanda `*` yang berdiri sendiri aman.
			if i+1 < len(s) && s[i+1] != ' ' {
				if akhir := strings.IndexByte(s[i+1:], '*'); akhir > 0 && s[i+akhir] != ' ' {
					b.WriteString(ansiItalic + renderInline(s[i+1:i+1+akhir]) + ansiReset)
					i += 1 + akhir + 1
					continue
				}
			}
		}

		b.WriteByte(s[i])
		i++
	}

	return b.String()
}

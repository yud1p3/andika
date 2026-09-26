package rag

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// saranInstructionAwal adalah penanda stabil awal instruksi saran. Dipakai
// test untuk mengenali panggilan saran tanpa bergantung pada jumlah saran yang
// diminta — jumlah itu ikut tertulis di instruksi, jadi membandingkan
// instruksi penuh akan mematahkan test setiap kali SARAN_COUNT berubah.
const saranInstructionAwal = `Anda membantu pengguna menelusuri dokumen peraturan.`

// saranInstruction menyusun instruksi lengkap untuk model.
//
// Keluarannya sengaja satu pertanyaan per baris, bukan JSON. JSON yang rusak
// sedikit saja akan mematikan seluruh daftar, dan mengurai JSON menambah
// kopling ke schema SDK. Baris demi baris cukup untuk daftar pendek dan tetap
// bisa diselamatkan walau model menambahkan basa-basi.
func saranInstruction(jumlah int) string {
	return fmt.Sprintf(`%s
Berdasarkan pertanyaan pengguna dan kutipan dokumen di bawah, susun %d pertanyaan
lanjutan yang mungkin ingin ditanyakan pengguna berikutnya.

Aturan:
- Satu pertanyaan per baris.
- Tanpa penomoran, tanda hubung, atau tanda kutip.
- Singkat dan berdiri sendiri, bisa dipahami tanpa percakapan sebelumnya.
- Pakai istilah yang muncul di kutipan dokumen.
- Jangan mengulang pertanyaan pengguna.
- Balas hanya daftar pertanyaannya, tanpa pembuka atau penutup.`, saranInstructionAwal, jumlah)
}

// Saran menyusun pertanyaan lanjutan dari pertanyaan pengguna dan konteks
// potongan yang terambil.
//
// Fungsi ini murni: tanpa goroutine, tanpa keadaan bersama. Penjadwalan dan
// pembatalannya tanggung jawab pemanggil, supaya jalur saran bisa berjalan
// bersamaan dengan penyusunan jawaban tanpa mengunci apa pun.
//
// jumlah <= 0 atau konteks kosong berarti fitur dimatikan: hasilnya nil tanpa
// memanggil model sama sekali.
func Saran(ctx context.Context, cm model.BaseChatModel, pertanyaan, konteks string, jumlah int) ([]string, error) {
	if jumlah <= 0 || strings.TrimSpace(konteks) == "" {
		return nil, nil
	}

	msgs := []*schema.Message{
		schema.SystemMessage(saranInstruction(jumlah)),
		schema.UserMessage("pertanyaan pengguna: " + pertanyaan + "\n\nkutipan dokumen:\n" + konteks),
	}

	out, err := cm.Generate(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("menyusun saran pertanyaan: %w", err)
	}
	if out == nil {
		return nil, nil
	}

	return uraiSaran(out.Content, pertanyaan, jumlah), nil
}

// reAwalanSaran mencocokkan penomoran dan bullet di awal baris: "1.", "1)",
// "-", "*", "•". Model sering menambahkannya meski sudah dilarang.
var reAwalanSaran = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)])\s*`)

// uraiSaran mengubah keluaran model menjadi daftar pertanyaan bersih.
//
// Yang dibuang: penomoran dan bullet, tanda kutip di ujung baris, baris kosong,
// baris pembuka yang berakhiran titik dua ("Berikut beberapa pertanyaan:"),
// duplikat, dan pertanyaan pengguna sendiri. Berhenti setelah maks butir.
func uraiSaran(teks, pertanyaan string, maks int) []string {
	if maks <= 0 {
		return nil
	}

	kunciPengguna := normalkanSaran(pertanyaan)
	dilihat := make(map[string]bool)
	var hasil []string

	for _, baris := range strings.Split(teks, "\n") {
		baris = reAwalanSaran.ReplaceAllString(baris, "")
		baris = strings.TrimSpace(baris)
		baris = strings.Trim(baris, `"'`+"“”‘’")
		baris = strings.TrimSpace(baris)

		if baris == "" || strings.HasSuffix(baris, ":") {
			continue
		}

		kunci := normalkanSaran(baris)
		if kunci == "" || kunci == kunciPengguna || dilihat[kunci] {
			continue
		}

		dilihat[kunci] = true
		hasil = append(hasil, baris)

		if len(hasil) >= maks {
			break
		}
	}

	return hasil
}

// normalkanSaran merapikan teks jadi kunci pembanding: huruf kecil, spasi
// tunggal. Dipakai untuk mengenali duplikat dan pertanyaan pengguna sendiri
// yang ditulis ulang sedikit berbeda kapitalisasi atau spasinya.
func normalkanSaran(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

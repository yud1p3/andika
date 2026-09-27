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

// maksKonteksSaran membatasi panjang kutipan dokumen yang dikirim ke model saran.
//
// Saran hanya butuh gambaran topik, bukan seluruh isi potongan. Di jalur agen,
// konteks ini adalah gabungan bukti dari semua tool — termasuk yang akhirnya
// tidak dipakai menjawab — sehingga bisa jauh lebih panjang dari yang perlu.
// Memotongnya menghemat kuota dan menjaga fokus model.
const maksKonteksSaran = 4000

// maksRiwayatSaran membatasi berapa pertanyaan lama yang dicantumkan ke model.
//
// Penyaringan hasil memakai daftar penuh — membandingkan teks jauh lebih murah
// daripada mengirimkannya. Batas ini hanya untuk prompt, supaya percakapan
// panjang tidak membengkakkan permintaan.
const maksRiwayatSaran = 10

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
- Jangan mengulang pertanyaan yang sudah pernah ditanyakan, bila daftarnya disertakan.
- Balas hanya daftar pertanyaannya, tanpa pembuka atau penutup.`, saranInstructionAwal, jumlah)
}

// Saran menyusun pertanyaan lanjutan dari pertanyaan pengguna, konteks potongan
// yang terambil, dan pertanyaan-pertanyaan yang sudah diajukan sebelumnya.
//
// sudahDitanya mencegah tombol yang muncul menawarkan hal yang sudah dijawab
// pada giliran sebelumnya. Tanpa itu, percakapan berlapis cenderung menampilkan
// saran yang sama berulang kali: konteks dokumennya tidak berubah antar-giliran,
// jadi model pun mengusulkan hal yang serupa lagi.
//
// Fungsi ini murni: tanpa goroutine, tanpa keadaan bersama. Penjadwalan dan
// pembatalannya tanggung jawab pemanggil, supaya jalur saran bisa berjalan
// bersamaan dengan penyusunan jawaban tanpa mengunci apa pun.
//
// jumlah <= 0 atau konteks kosong berarti fitur dimatikan: hasilnya nil tanpa
// memanggil model sama sekali.
func Saran(ctx context.Context, cm model.BaseChatModel, pertanyaan, konteks string, jumlah int, sudahDitanya []string) ([]string, error) {
	if jumlah <= 0 || strings.TrimSpace(konteks) == "" {
		return nil, nil
	}

	msgs := []*schema.Message{
		schema.SystemMessage(saranInstruction(jumlah)),
		schema.UserMessage(pesanSaran(pertanyaan, konteks, sudahDitanya)),
	}

	out, err := cm.Generate(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("menyusun saran pertanyaan: %w", err)
	}
	if out == nil {
		return nil, nil
	}

	return uraiSaran(out.Content, pertanyaan, sudahDitanya, jumlah), nil
}

// pesanSaran menyusun pesan user untuk model saran.
//
// Dua pemangkasan terjadi di sini. Kutipan dokumen dipotong lebih dulu (lihat
// maksKonteksSaran), dan daftar pertanyaan lama dibatasi ke ekor terakhir —
// yang paling relevan untuk mencegah pengulangan pada percakapan panjang.
func pesanSaran(pertanyaan, konteks string, sudahDitanya []string) string {
	var b strings.Builder

	b.WriteString("pertanyaan pengguna: ")
	b.WriteString(pertanyaan)

	if lama := potongEkor(sudahDitanya, maksRiwayatSaran); len(lama) > 0 {
		b.WriteString("\n\npertanyaan yang sudah pernah ditanyakan (jangan diulang):")
		for _, q := range lama {
			b.WriteString("\n- ")
			b.WriteString(q)
		}
	}

	b.WriteString("\n\nkutipan dokumen:\n")
	b.WriteString(potongKonteks(konteks, maksKonteksSaran))

	return b.String()
}

// potongEkor mengambil paling banyak maks elemen terakhir, atau seluruhnya bila
// sudah cukup pendek. maks <= 0 berarti tidak ada yang diambil.
func potongEkor(teks []string, maks int) []string {
	if maks <= 0 {
		return nil
	}
	if len(teks) <= maks {
		return teks
	}
	return teks[len(teks)-maks:]
}

// potongKonteks memotong kutipan dokumen supaya tidak melebihi maks karakter.
//
// Dipotong pada batas potongan (dua baris baru) supaya kalimatnya tidak putus
// di tengah. Dihitung per rune, bukan per byte, supaya karakter ber-UTF-8 tidak
// terbelah. maks <= 0 berarti tanpa batas.
func potongKonteks(konteks string, maks int) string {
	if maks <= 0 || len([]rune(konteks)) <= maks {
		return konteks
	}

	var (
		b     strings.Builder
		total int
		ada   bool
	)
	for i, bagian := range strings.Split(konteks, "\n\n") {
		biaya := len([]rune(bagian))
		if i > 0 {
			biaya += 2 // pemisah "\n\n" yang ikut tersimpan
		}
		if total+biaya > maks {
			break
		}

		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(bagian)
		total += biaya
		ada = true
	}

	// Potongan pertama pun tidak muat: tidak ada yang bisa disimpan utuh, jadi
	// dipotong apa adanya daripada mengembalikan kosong.
	if !ada {
		return string([]rune(konteks)[:maks])
	}
	return b.String()
}

// reAwalanSaran mencocokkan penomoran dan bullet di awal baris: "1.", "1)",
// "-", "*", "•". Model sering menambahkannya meski sudah dilarang.
var reAwalanSaran = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)])\s*`)

// uraiSaran mengubah keluaran model menjadi daftar pertanyaan bersih.
//
// Yang dibuang: penomoran dan bullet, tanda kutip di ujung baris, baris kosong,
// baris pembuka yang berakhiran titik dua ("Berikut beberapa pertanyaan:"),
// duplikat, pertanyaan pengguna sendiri, dan pertanyaan yang sudah pernah
// ditanyakan (sudahDitanya). Berhenti setelah maks butir.
//
// Daftar sudahDitanya dipakai utuh di sini, tanpa dipotong seperti di prompt:
// membandingkan teks jauh lebih murah daripada mengirimkannya ke model, dan
// makin lengkap daftarnya makin kecil peluang pengulangan lolos.
func uraiSaran(teks, pertanyaan string, sudahDitanya []string, maks int) []string {
	if maks <= 0 {
		return nil
	}

	terlarang := make(map[string]bool, len(sudahDitanya)+1)
	terlarang[normalkanSaran(pertanyaan)] = true
	for _, q := range sudahDitanya {
		terlarang[normalkanSaran(q)] = true
	}

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
		if kunci == "" || terlarang[kunci] || dilihat[kunci] {
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

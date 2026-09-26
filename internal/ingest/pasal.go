// Splitter sadar-struktur: memotong dokumen peraturan pada batas Pasal.
//
// Kenapa bukan splitter berbasis ukuran seperti di notebook? Karena pemisah
// default recursive splitter adalah ["\n", ".", "?", "!"] — ia memotong di
// tengah paragraf pada batas kalimat, bukan pada batas struktur. Akibatnya satu
// pasal tercabik jadi banyak potongan, dan hanya potongan pertama yang masih
// membawa tulisan "Pasal N". Potongan sisanya tidak bisa ditemukan lewat
// rujukan pasal, berapa pun TOP_K-nya.
//
// Contoh nyata sebelum perbaikan ini: Pasal 1 (2.858 karakter, 17 definisi)
// terpecah menjadi 8 chunk, dan 7 di antaranya tanpa penanda pasal sama sekali.
//
// Unit yang dipakai di sini adalah pasal, bukan ayat. Per-ayat terlalu halus:
// median satu ayat hanya 160 karakter, 43% di antaranya di bawah 150 karakter
// sehingga vektornya nyaris tidak bermakna, 60 dari 149 pasal tidak punya ayat
// sama sekali, dan ayat saling merujuk ("sebagaimana dimaksud pada ayat (1)")
// sehingga berdiri sendiri menjadi kalimat yang menggantung.
//
// Ayat tetap dipakai, tapi hanya sebagai titik potong ketika satu pasal tidak
// muat — dan setiap potongan hasilnya diberi header rujukan supaya pasalnya
// tetap bisa ditemukan.
package ingest

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/schema"

	"rag-eino/internal/config"
)

// Pola penanda struktur. Semuanya memakai (?m) supaya ^ cocok di awal baris,
// bukan hanya di awal teks.
var (
	// rePasal cocok dengan baris penanda pasal. Isinya boleh menyambung di
	// baris yang sama, misalnya "Pasal 7 Jenis Naskah Dinas ... terdiri
	// atas:", karena konversi PDF kadang tidak memberi baris sendiri untuk
	// penandanya.
	//
	// Baris seperti itu tetap harus dikenali. Tanpa itu, isi pasalnya menempel
	// ke pasal sebelumnya dan nomor pasalnya menguap — termasuk Pasal 7, yang
	// justru memuat pengelompokan naskah dinas tingkat atas.
	//
	// Longgarnya pola ini tidak berbahaya karena nomor pasal dijaga menaik;
	// lihat penjagaan di bagiSegmen. Baris rujukan yang terlempar ke awal
	// baris — "Pasal 112 Huruf (a) digunakan untuk ...", potongan kalimat dari
	// Pasal 113 — tetap ditolak di sana karena nomornya lebih kecil.
	rePasal = regexp.MustCompile(`(?m)^Pasal (\d+)(?:\s|$)`)
	// rePasalSendiri cocok hanya kalau barisnya persis "Pasal 12", tanpa isi.
	// Dipakai untuk membuang baris penanda, dan hanya boleh membuang yang
	// benar-benar kosong isinya.
	rePasalSendiri = regexp.MustCompile(`^Pasal \d+$`)
	// reBAB cocok dengan baris "BAB IV". Judulnya ada di baris berikutnya.
	reBAB = regexp.MustCompile(`(?m)^BAB ([IVXLCDM]+)$`)
	// reLampiran menandai awal lampiran, yang bukan lagi batang tubuh.
	reLampiran = regexp.MustCompile(`(?m)^LAMPIRAN `)
	// reAyat cocok dengan awal ayat: "- (3) ...".
	reAyat = regexp.MustCompile(`(?m)^- \(\d+\)`)
	// reButir cocok dengan awal butir bernomor seperti definisi di Pasal 1:
	// "- 5\. ...". Backslash-nya memang ada di Markdown, hasil konversi xberg.
	reButir = regexp.MustCompile(`(?m)^- \d+\\\.`)
	// reNomorAyat dan reNomorButir mengambil nomornya saja, untuk label.
	reNomorAyat  = regexp.MustCompile(`^- \((\d+)\)`)
	reNomorButir = regexp.MustCompile(`^- (\d+)\\\.`)
)

// Kunci metadata yang disimpan ke Qdrant.
//
// Semua nilainya WAJIB bertipe string. Indexer Qdrant memanggil
// qdrant.NewValueMap, yang memanggil NewValue, dan NewValue PANIC — bukan
// mengembalikan error — untuk tipe yang tidak dikenal. Tipe liar di sini akan
// menjatuhkan proses ingest, bukan melaporkan kesalahan yang bisa ditelusuri.
const (
	metaBagian   = "bagian"
	metaPasal    = "pasal"
	metaAyat     = "ayat"
	metaBAB      = "bab"
	metaBABJudul = "bab_judul"
)

// Nilai untuk metaBagian.
const (
	bagianPembukaan   = "pembukaan"
	bagianBatangTubuh = "batang_tubuh"
	bagianLampiran    = "lampiran"
	bagianTanpaPasal  = "tanpa_pasal"
)

// segmen adalah satu unit dokumen yang akan menjadi satu atau lebih chunk.
type segmen struct {
	bagian   string
	pasal    string // kosong kalau bukan pasal
	bab      string
	babJudul string
	isi      string
}

// unitPasal adalah satu bagian di dalam pasal yang tidak boleh dipotong lagi:
// satu ayat, satu butir bernomor, atau pengantar sebelum keduanya.
type unitPasal struct {
	// label berisi nomor ayat/butir, kosong untuk teks pengantar.
	label string
	isi   string
}

// pemotong menyatukan konfigurasi dan splitter cadangan supaya splitter tidak
// dibuat ulang untuk setiap pemanggilan.
type pemotong struct {
	cfg      *config.Config
	cadangan document.Transformer
}

// potongPerPasal memotong dokumen pada batas Pasal.
//
// Dokumen yang tidak punya penanda pasal sama sekali — misalnya PDF yang dibaca
// per halaman — jatuh ke recursive splitter, bukan menghasilkan error. Dengan
// begitu mengganti DOC_PATH ke berkas non-peraturan tetap bekerja.
func potongPerPasal(ctx context.Context, cfg *config.Config, docs []*schema.Document) ([]*schema.Document, error) {
	splitter, err := recursiveSplitter(ctx, cfg)
	if err != nil {
		return nil, err
	}

	p := pemotong{cfg: cfg, cadangan: splitter}

	var hasil []*schema.Document
	for _, doc := range docs {
		bagian := bagiSegmen(doc.Content)
		if bagian == nil {
			chunks, err := p.potongCadangan(ctx, doc.Content)
			if err != nil {
				return nil, err
			}
			for _, c := range chunks {
				c.MetaData = metadata(bagianTanpaPasal, "", "", "", "")
			}
			hasil = append(hasil, chunks...)
			continue
		}

		for _, s := range bagian {
			chunks, err := p.potongSegmen(ctx, s)
			if err != nil {
				return nil, err
			}
			hasil = append(hasil, chunks...)
		}
	}

	return hasil, nil
}

// bagiSegmen membagi teks dokumen menjadi pembukaan, satu segmen per pasal,
// dan lampiran.
//
// Mengembalikan nil kalau dokumennya tidak punya penanda pasal sama sekali.
func bagiSegmen(teks string) []segmen {
	baris := strings.Split(teks, "\n")

	// Titik potong beserta keadaan struktur pada titik itu.
	type titik struct {
		baris    int
		bagian   string
		pasal    string
		bab      string
		babJudul string
	}

	var (
		titikPotong []titik
		bab         string
		babJudul    string
		// Judul BAB ada di baris tidak kosong setelah penanda BAB.
		tungguJudul bool
		adaLampiran bool
		// Nomor pasal terakhir yang diterima, untuk menolak penanda palsu.
		pasalTerakhir int
		adaPasal      bool
	)

	for i, b := range baris {
		t := strings.TrimSpace(b)

		if tungguJudul && t != "" {
			tungguJudul = false
			// Penanda struktur tidak boleh ditelan sebagai judul BAB. Bisa
			// terjadi kalau sebuah BAB langsung diikuti Pasal tanpa judul, dan
			// tanpa penjagaan ini penanda pasalnya ikut hilang.
			if !rePasal.MatchString(t) && !reBAB.MatchString(t) && !reLampiran.MatchString(t) {
				babJudul = t
				continue
			}
		}

		if m := reBAB.FindStringSubmatch(t); m != nil {
			bab = m[1]
			babJudul = ""
			tungguJudul = true
			continue
		}

		if m := rePasal.FindStringSubmatch(t); m != nil {
			n, err := strconv.Atoi(m[1])
			// Nomor pasal di batang tubuh selalu naik. Baris "Pasal N" yang
			// nomornya tidak lebih besar dari pasal terakhir hampir pasti bukan
			// judul pasal, melainkan potongan rujukan yang terlempar ke baris
			// sendiri saat PDF dikonversi.
			//
			// Contoh nyatanya ada di dokumen ini: di tengah Pasal 89 muncul
			// baris "Pasal 8" dari tabel yang rusak. Tanpa penjagaan ini ia
			// menjadi chunk tersendiri berlabel pasal=8, menutupi Pasal 8 yang
			// asli, dan isinya kata-kata acak.
			//
			// Celah nomor tetap diterima — dokumen ini memang melompat dari 149
			// ke 150 tanpa beberapa nomor.
			if err != nil || (adaPasal && n <= pasalTerakhir) {
				continue
			}
			pasalTerakhir = n
			adaPasal = true

			bagian := bagianBatangTubuh
			if adaLampiran {
				bagian = bagianLampiran
			}

			titikPotong = append(titikPotong, titik{
				baris:    i,
				bagian:   bagian,
				pasal:    m[1],
				bab:      bab,
				babJudul: babJudul,
			})
			continue
		}

		// Lampiran hanya diakui setelah ada minimal satu pasal, supaya kata
		// "LAMPIRAN" di bagian menimbang tidak salah dianggap batas.
		if !adaLampiran && len(titikPotong) > 0 && reLampiran.MatchString(t) {
			adaLampiran = true
			titikPotong = append(titikPotong, titik{baris: i, bagian: bagianLampiran})
		}
	}

	if len(titikPotong) == 0 {
		return nil
	}

	var hasil []segmen

	// Teks sebelum penanda pasal pertama: bagian menimbang, mengingat, dan
	// memutuskan.
	if awal := titikPotong[0].baris; awal > 0 {
		if isi := strings.TrimSpace(strings.Join(baris[:awal], "\n")); isi != "" {
			hasil = append(hasil, segmen{bagian: bagianPembukaan, isi: isi})
		}
	}

	for n, tp := range titikPotong {
		akhir := len(baris)
		if n+1 < len(titikPotong) {
			akhir = titikPotong[n+1].baris
		}

		isi := strings.TrimSpace(strings.Join(baris[tp.baris:akhir], "\n"))
		if isi == "" {
			continue
		}

		hasil = append(hasil, segmen{
			bagian:   tp.bagian,
			pasal:    tp.pasal,
			bab:      tp.bab,
			babJudul: tp.babJudul,
			isi:      isi,
		})
	}

	return hasil
}

// potongSegmen mengubah satu segmen menjadi satu atau lebih chunk.
func (p pemotong) potongSegmen(ctx context.Context, s segmen) ([]*schema.Document, error) {
	// Pembukaan dan lampiran tidak berpasal, jadi dipotong berdasarkan ukuran.
	if s.pasal == "" {
		chunks, err := p.potongCadangan(ctx, s.isi)
		if err != nil {
			return nil, err
		}
		for _, c := range chunks {
			c.MetaData = metadata(s.bagian, "", "", s.bab, s.babJudul)
		}
		return chunks, nil
	}

	// Pasal yang muat disimpan utuh. Ini berlaku untuk sekitar 69% pasal, dan
	// merupakan bentuk terbaik: isinya lengkap dan tidak ada header buatan.
	if len(s.isi) <= p.cfg.ChunkSize {
		return []*schema.Document{chunkPasal(s, s.isi, "")}, nil
	}

	// Cari batas internal. Ayat lebih dulu; kalau pasalnya tidak berayat,
	// butir bernomor seperti daftar definisi di Pasal 1.
	batas, jenis := reAyat, "ayat"
	if !batas.MatchString(s.isi) {
		batas, jenis = reButir, "angka"
	}
	if !batas.MatchString(s.isi) {
		// Tidak ada batas internal sama sekali. Dipotong berdasarkan ukuran,
		// tetapi setiap potongan tetap diberi header supaya rujukan pasalnya
		// tidak hilang — inilah bug yang sedang diperbaiki.
		return p.potongTanpaBatas(ctx, s)
	}

	kelompok := kemasUnit(pecahUnit(buangJudulPasal(s.isi), batas), p.cfg.ChunkSize)

	var hasil []*schema.Document
	for n, k := range kelompok {
		// Satu unit yang sendirian melebihi batas dipecah lagi berdasarkan
		// ukuran; header tetap dipasang setelahnya.
		if len(k) == 1 && len(k[0].isi) > p.cfg.ChunkSize {
			sub, err := p.potongUnitBesar(ctx, s, k[0], jenis)
			if err != nil {
				return nil, err
			}
			hasil = append(hasil, sub...)
			continue
		}

		header := headerPasal(s, k, n+1, len(kelompok), jenis)
		isi := header + "\n\n" + strings.Join(isiUnit(k), "\n\n")
		hasil = append(hasil, chunkPasal(s, isi, rentangLabel(k)))
	}

	return hasil, nil
}

// potongUnitBesar memecah satu ayat/butir yang sendirian sudah melebihi batas.
func (p pemotong) potongUnitBesar(ctx context.Context, s segmen, u unitPasal, jenis string) ([]*schema.Document, error) {
	chunks, err := p.potongCadangan(ctx, u.isi)
	if err != nil {
		return nil, err
	}

	hasil := make([]*schema.Document, 0, len(chunks))
	for i, c := range chunks {
		header := fmt.Sprintf("Pasal %s (bagian %d/%d)", s.pasal, i+1, len(chunks))
		if u.label != "" {
			header = fmt.Sprintf("Pasal %s %s (%s), bagian %d/%d",
				s.pasal, jenis, u.label, i+1, len(chunks))
		}
		isi := header + "\n\n" + strings.TrimSpace(c.Content)
		hasil = append(hasil, chunkPasal(s, isi, u.label))
	}
	return hasil, nil
}

// potongTanpaBatas memotong pasal panjang yang tidak punya batas ayat maupun
// butir, dengan tetap memberi header rujukan pada setiap potongan.
func (p pemotong) potongTanpaBatas(ctx context.Context, s segmen) ([]*schema.Document, error) {
	chunks, err := p.potongCadangan(ctx, buangJudulPasal(s.isi))
	if err != nil {
		return nil, err
	}

	hasil := make([]*schema.Document, 0, len(chunks))
	for i, c := range chunks {
		header := fmt.Sprintf("Pasal %s (bagian %d/%d)", s.pasal, i+1, len(chunks))
		isi := header + "\n\n" + strings.TrimSpace(c.Content)
		hasil = append(hasil, chunkPasal(s, isi, ""))
	}
	return hasil, nil
}

// potongCadangan memotong teks dengan recursive splitter. Document yang
// dikembalikan belum punya metadata; itu diisi pemanggil.
func (p pemotong) potongCadangan(ctx context.Context, teks string) ([]*schema.Document, error) {
	chunks, err := p.cadangan.Transform(ctx, []*schema.Document{{Content: teks}})
	if err != nil {
		return nil, fmt.Errorf("memotong bagian tanpa pasal: %w", err)
	}
	return chunks, nil
}

// buangJudulPasal membuang baris penanda "Pasal N" di awal isi segmen.
//
// Dibuang karena setiap potongan hasil pemecahan akan diberi header sendiri
// yang menyebut pasal sekaligus rentangnya.
//
// Hanya baris yang isinya persis penanda yang dibuang. Penanda yang menyambung
// dengan isinya — "Pasal 7 Jenis Naskah Dinas ... terdiri atas:" — tidak
// disentuh, karena membuangnya berarti membuang kalimat pengantar pasalnya.
func buangJudulPasal(isi string) string {
	baris := strings.SplitN(isi, "\n", 2)
	if len(baris) == 2 && rePasalSendiri.MatchString(strings.TrimSpace(baris[0])) {
		return strings.TrimSpace(baris[1])
	}
	return isi
}

// pecahUnit memecah isi pasal pada setiap batas, dengan teks sebelum batas
// pertama menjadi unit pengantar.
func pecahUnit(isi string, batas *regexp.Regexp) []unitPasal {
	lokasi := batas.FindAllStringIndex(isi, -1)
	if len(lokasi) == 0 {
		return []unitPasal{{isi: isi}}
	}

	var unit []unitPasal
	if pengantar := strings.TrimSpace(isi[:lokasi[0][0]]); pengantar != "" {
		unit = append(unit, unitPasal{isi: pengantar})
	}

	for i, l := range lokasi {
		akhir := len(isi)
		if i+1 < len(lokasi) {
			akhir = lokasi[i+1][0]
		}
		potongan := strings.TrimSpace(isi[l[0]:akhir])
		if potongan == "" {
			continue
		}
		unit = append(unit, unitPasal{label: labelUnit(potongan), isi: potongan})
	}

	return unit
}

// labelUnit mengambil nomor ayat atau butir dari awal sebuah unit.
func labelUnit(teks string) string {
	for _, re := range []*regexp.Regexp{reNomorAyat, reNomorButir} {
		if m := re.FindStringSubmatch(teks); m != nil {
			return m[1]
		}
	}
	return ""
}

// kemasUnit mengelompokkan unit secara berurutan selama totalnya masih muat.
//
// Unit tidak pernah dipisah antar kelompok, dan urutannya dipertahankan, supaya
// satu ayat tidak pernah terbelah dua.
func kemasUnit(unit []unitPasal, batas int) [][]unitPasal {
	var kelompok [][]unitPasal
	var sekarang []unitPasal
	panjang := 0

	for _, u := range unit {
		if len(sekarang) > 0 && panjang+len(u.isi) > batas {
			kelompok = append(kelompok, sekarang)
			sekarang = nil
			panjang = 0
		}
		sekarang = append(sekarang, u)
		panjang += len(u.isi) + 1
	}
	if len(sekarang) > 0 {
		kelompok = append(kelompok, sekarang)
	}

	return kelompok
}

// isiUnit menggabungkan isi unit dalam satu kelompok.
func isiUnit(kelompok []unitPasal) []string {
	isi := make([]string, 0, len(kelompok))
	for _, u := range kelompok {
		isi = append(isi, u.isi)
	}
	return isi
}

// rentangLabel menyusun label rentang ayat/butir untuk metadata, misalnya
// "2-4". Kosong kalau kelompoknya tidak berayat.
func rentangLabel(kelompok []unitPasal) string {
	var nomor []string
	for _, u := range kelompok {
		if u.label != "" {
			nomor = append(nomor, u.label)
		}
	}

	switch len(nomor) {
	case 0:
		return ""
	case 1:
		return nomor[0]
	default:
		return nomor[0] + "-" + nomor[len(nomor)-1]
	}
}

// headerPasal menyusun baris rujukan di awal potongan hasil pemecahan.
//
// Ini bagian terpenting dari seluruh berkas ini. Tanpa header, potongan kedua
// dan seterusnya dari sebuah pasal tidak lagi memuat tulisan "Pasal N", sehingga
// tidak bisa ditemukan lewat rujukan pasal — persis bug yang diperbaiki.
func headerPasal(s segmen, kelompok []unitPasal, urutan, total int, jenis string) string {
	label := rentangLabel(kelompok)

	// Kelompok tanpa label sama sekali (hanya teks pengantar) tidak punya
	// rentang yang bisa disebut, jadi memakai nomor urut potongan.
	if label == "" {
		return fmt.Sprintf("Pasal %s (bagian %d/%d)", s.pasal, urutan, total)
	}

	if i := strings.Index(label, "-"); i >= 0 {
		return fmt.Sprintf("Pasal %s %s (%s)-(%s)", s.pasal, jenis, label[:i], label[i+1:])
	}
	return fmt.Sprintf("Pasal %s %s (%s)", s.pasal, jenis, label)
}

// chunkPasal membuat Document untuk satu potongan pasal.
func chunkPasal(s segmen, isi, ayat string) *schema.Document {
	return &schema.Document{
		Content:  isi,
		MetaData: metadata(s.bagian, s.pasal, ayat, s.bab, s.babJudul),
	}
}

// metadata menyusun metadata chunk. Semua nilainya string; lihat catatan pada
// konstanta meta* soal kenapa itu wajib.
func metadata(bagian, pasal, ayat, bab, babJudul string) map[string]any {
	return map[string]any{
		metaBagian:   bagian,
		metaPasal:    pasal,
		metaAyat:     ayat,
		metaBAB:      bab,
		metaBABJudul: babJudul,
	}
}

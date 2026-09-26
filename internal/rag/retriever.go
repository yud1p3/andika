package rag

import (
	"context"
	"log"
	"regexp"

	qdrantretriever "github.com/cloudwego/eino-ext/components/retriever/qdrant"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// reRujukanPasal menangkap rujukan pasal di dalam teks: "pasal 1", "Pasal
// 139", "pasal 89 ayat (2)".
//
// Dipakai di dua tempat: membaca nomor pasal yang disebut di pertanyaan, dan
// memindai isi pasal yang sudah terambil untuk menemukan rujukan silangnya
// (lihat nomorRujukanSilang).
//
// Hanya nomornya yang diambil. Rujukan ke ayat atau huruf tidak dipakai untuk
// menyaring, karena satu pasal jarang dipecah sampai ke tingkat itu.
//
// Polanya sengaja selebar \bpasal\s+(\d+) alih-alih mengunci frasa baku
// "sebagaimana dimaksud dalam Pasal". Terukur pada dokumen contoh: frasa baku
// menangkap 45 rujukan, pola longgar 49. Empat sisanya memakai frasa lain —
// "sebagaimana dimaksud Pasal 27 huruf b", "sebagaimana dimaksud pada Pasal 106
// huruf a", dan satu yang salah ketik ("dimaksudd alam Pasal 27") sehingga
// tidak akan tertangkap frasa baku mana pun. Semuanya rujukan yang sah, jadi
// mengetatkan pola di sini berarti kehilangan rujukan.
var reRujukanPasal = regexp.MustCompile(`(?i)\bpasal\s+(\d+)`)

// Kunci di dalam metadata potongan yang menyimpan nomor pasal. Nilainya
// ditulis oleh internal/ingest (lihat pasal.go); di sini hanya dibaca.
const kunciPasal = "pasal"

// bidangPasal adalah alamat lengkap nomor pasal di dalam payload Qdrant.
//
// Metadata tidak disimpan sebagai kunci tingkat atas, melainkan dikumpulkan di
// bawah kunci "metadata" — jadi alamatnya "metadata.pasal".
const bidangPasal = kunciPayloadMetadata + "." + kunciPasal

// Kunci metadata sementara untuk menandai asal sebuah potongan. Hanya dipakai
// di dalam proses; tidak ikut tersimpan ke Qdrant.
const kunciAsal = "asal"

// Nilai untuk kunciAsal.
const (
	asalPasal   = "pasal"
	asalRujukan = "rujukan"
	asalVektor  = "vektor"
)

// topKPasal adalah jumlah potongan yang diambil saat menyaring per pasal.
//
// Nilainya jauh di atas kebutuhan: pasal terpanjang di dokumen contoh hanya
// terpecah menjadi 7 potongan. Kelonggaran ini disengaja — yang diambil adalah
// seluruh isi satu pasal, dan kehilangan sebagiannya persis masalah yang
// sedang diperbaiki di sini.
const topKPasal = 20

// maksRujukanSilang adalah jumlah maksimal pasal tambahan yang diikuti dari
// rujukan silang dalam satu pencarian.
//
// Terukur pada dokumen contoh: tidak ada satu pun pasal yang merujuk lebih dari
// satu pasal lain, jadi batas ini tidak pernah tercapai di sana. Batas tetap
// dipasang untuk dokumen lain yang rujukannya lebih rapat — konteks yang
// membanjir lebih merugikan jawaban daripada satu rujukan yang tidak diikuti.
const maksRujukanSilang = 4

// retrieverPasal melengkapi pencarian vektor dengan pencarian langsung
// berdasarkan nomor pasal yang disebut di pertanyaan.
//
// Kenapa perlu. Pencarian vektor menilai kemiripan makna, sedangkan "pasal 1
// peraturan ini membahas apa" adalah pertanyaan tentang **rujukan**, bukan
// tentang topik. Semua potongan yang memuat kata "Pasal" berskor hampir sama,
// dan yang menang belum tentu pasal yang dimaksud.
//
// Terukur pada dokumen contoh: Pasal 1 terpecah menjadi 7 potongan, dan hanya
// **3 di antaranya masuk 100 besar** hasil pencarian vektor — 4 sisanya tidak
// terjangkau berapa pun TOP_K-nya. Setelah perbaikan chunking, potongan yang
// membawa definisi 1-5 memang naik ke peringkat 7, tetapi jawabannya tetap
// hanya memuat 5 dari 17 definisi.
//
// Penyaringan metadata menutup celah itu: begitu pertanyaan menyebut nomor
// pasal, seluruh potongan pasal tersebut diambil langsung, tanpa bergantung
// pada kedekatan vektor.
//
// Satu langkah lagi ditambahkan di atasnya: pasal yang terambil dipindai untuk
// rujukan silangnya, lalu pasal yang dirujuk ikut diambil. Lihat ikutiRujukan.
type retrieverPasal struct {
	dasar retriever.Retriever
}

// newRetrieverPasal membungkus retriever dengan penyaringan per pasal.
func newRetrieverPasal(dasar retriever.Retriever) *retrieverPasal {
	return &retrieverPasal{dasar: dasar}
}

// Retrieve menjalankan pencarian vektor, lalu menambahkan seluruh potongan
// pasal yang dirujuk di depan hasilnya — berikut pasal yang dirujuk oleh
// pasal-pasal itu sendiri.
//
// Hasil penyaringan diletakkan di depan karena itulah yang ditanyakan. Hasil
// vektor tetap disertakan di belakangnya supaya konteks lain — termasuk
// Lampiran yang memuat format terkait — tidak hilang.
func (r *retrieverPasal) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	vektor, err := r.dasar.Retrieve(ctx, query, opts...)
	if err != nil {
		return nil, err
	}

	nomor := nomorPasalDirujuk(query)
	if len(nomor) == 0 {
		return vektor, nil
	}

	pasal, err := r.cariPerPasal(ctx, query, nomor, opts)
	if err != nil {
		// Penyaringan yang gagal bukan alasan menjatuhkan seluruh pencarian:
		// hasil vektor sudah cukup untuk menjawab banyak pertanyaan.
		log.Printf("rag: pencarian per pasal gagal, memakai hasil vektor saja: %v", err)
		return vektor, nil
	}
	if len(pasal) == 0 {
		// Nomor pasal yang tidak ada di dokumen — misalnya pertanyaan tentang
		// peraturan lain. Hasil vektor tetap dipakai.
		log.Printf("rag: tidak ada potongan untuk pasal %v, memakai hasil vektor saja", nomor)
		return vektor, nil
	}

	lanjutan := r.ikutiRujukan(ctx, query, pasal, nomor, opts)

	log.Printf("rag: rujukan pasal %v — %d potongan pasal diletakkan di depan %d hasil vektor",
		nomor, len(pasal), len(vektor))

	return r.gabung(
		kelompok{pasal, asalPasal},
		kelompok{lanjutan, asalRujukan},
		kelompok{vektor, asalVektor},
	), nil
}

// cariPerPasal mengambil seluruh potongan milik pasal-pasal yang disebut.
func (r *retrieverPasal) cariPerPasal(ctx context.Context, query string, nomor []string, opts []retriever.Option) ([]*schema.Document, error) {
	// Opsi disalin, tidak ditambahi di tempat. Menambahkan langsung ke opts
	// akan menulis ke slice milik pemanggil, dan pada "rag cari -topk N" itu
	// berarti TOP_K milik pemanggil ikut berubah.
	opsi := make([]retriever.Option, 0, len(opts)+2)
	opsi = append(opsi, opts...)
	opsi = append(opsi,
		retriever.WithTopK(topKPasal),
		qdrantretriever.WithFilter(filterPasal(nomor)),
	)

	return r.dasar.Retrieve(ctx, query, opsi...)
}

// kelompok adalah sekumpulan potongan beserta penanda asalnya, dipakai sebagai
// masukan gabung.
type kelompok struct {
	docs []*schema.Document
	asal string
}

// gabung menggabungkan kelompok potongan menurut urutan kepentingannya — yang
// lebih dulu disebut lebih dulu — dan potongan yang muncul di beberapa
// kelompok hanya disimpan sekali.
//
// Asal potongan ditandai di sini, pada saat pertama kali ia terlihat. Menandai
// sebelum penggabungan akan salah untuk potongan yang ditemukan kedua cara:
// penanda yang terakhir menimpanya.
func (r *retrieverPasal) gabung(semua ...kelompok) []*schema.Document {
	total := 0
	for _, k := range semua {
		total += len(k.docs)
	}

	hasil := make([]*schema.Document, 0, total)
	dilihat := make(map[string]struct{}, total)

	for _, k := range semua {
		for _, d := range k.docs {
			if _, ada := dilihat[d.ID]; ada {
				continue
			}
			dilihat[d.ID] = struct{}{}

			if d.MetaData == nil {
				d.MetaData = make(map[string]any, 1)
			}
			d.MetaData[kunciAsal] = k.asal

			hasil = append(hasil, d)
		}
	}

	return hasil
}

// ikutiRujukan menelusuri rujukan silang satu lompatan dari potongan pasal
// yang sudah terambil.
//
// Kenapa perlu. Terukur pada dokumen contoh: 49 dari 155 pasal (32%) memuat
// rujukan ke pasal lain, dan rujukan itu bukan hiasan. Pertanyaan "Pasal 145
// mengatur apa?" mengambil Pasal 145, sedangkan isi Pasal 145 menunjuk Pasal
// 25 — dan Pasal 25 tidak ikut terambil, karena pencarian vektor tidak punya
// alasan untuk mendekatkannya. Model lalu menjawab tanpa bahan yang justru
// dirujuk. Hal yang sama terjadi pada "Pasal 33 mengatur apa?": Pasal 33
// menyebut "Surat kuasa sebagaimana dimaksud Pasal 27 huruf b", sementara
// Pasal 27 tidak ada di konteks.
//
// Satu lompatan saja. Rujukan dari pasal hasil ikutan tidak diikuti lagi:
// setiap lompatan menambah satu perjalanan ke Qdrant dan memperbesar konteks,
// sementara di dokumen contoh rujukan berantai tidak muncul — setiap pasal
// merujuk paling banyak satu pasal lain.
//
// Hanya potongan dari kelompok pasal yang dipindai, bukan hasil vektor.
// Potongan hasil vektor adalah pecahan yang bisa datang dari mana saja, dan
// memindainya akan menarik pasal yang banyak dirujuk ke pertanyaan yang tidak
// membutuhkannya. Terukur: pertanyaan "Apa itu lembar disposisi?" ikut
// mengambil potongan Pasal 57 — yang menyebut "sebagaimana dimaksud dalam
// Pasal 27 huruf n" — sehingga pemindaian hasil vektor akan menambahkan
// seluruh enumerasi jenis naskah dinas ke pertanyaan tentang lembar disposisi,
// padahal jawabannya sudah utuh di Pasal 24 dan Pasal 19.
//
// Kegagalan di sini tidak pernah menjatuhkan pencarian: yang dikembalikan
// hanya potongan tambahan, dan pemanggil tetap memakai hasil pasal serta hasil
// vektor apa adanya.
func (r *retrieverPasal) ikutiRujukan(ctx context.Context, query string, pasal []*schema.Document, nomor []string, opts []retriever.Option) []*schema.Document {
	rujukan := nomorRujukanSilang(pasal, nomor)
	if len(rujukan) == 0 {
		return nil
	}

	lanjutan, err := r.cariPerPasal(ctx, query, rujukan, opts)
	if err != nil {
		log.Printf("rag: menelusuri rujukan silang %v gagal, memakai hasil pasal saja: %v", rujukan, err)
		return nil
	}
	if len(lanjutan) == 0 {
		log.Printf("rag: rujukan silang %v tidak menghasilkan potongan", rujukan)
		return nil
	}

	log.Printf("rag: rujukan silang dari pasal %v — pasal %v ikut diambil (%d potongan)",
		nomor, rujukan, len(lanjutan))

	return lanjutan
}

// nomorRujukanSilang mengumpulkan nomor pasal yang dirujuk oleh potongan yang
// sudah terambil, tanpa pengulangan, tanpa menyebut pasalnya sendiri, dan
// tidak lebih dari maksRujukanSilang.
//
// Rujukan ke pasal sendiri dibuang karena setiap potongan memuat nomor
// pasalnya di header — "Pasal 89 ayat (1)-(2)" — sehingga tanpa penyaringan
// ini setiap pasal akan dianggap merujuk dirinya sendiri.
//
// Urutannya mengikuti urutan potongan, jadi batas maksRujukanSilang memotong
// rujukan yang paling belakang, bukan yang sembarang.
func nomorRujukanSilang(docs []*schema.Document, sudahAda []string) []string {
	terpakai := make(map[string]struct{}, len(sudahAda)+maksRujukanSilang)
	for _, n := range sudahAda {
		terpakai[n] = struct{}{}
	}

	var nomor []string
	for _, d := range docs {
		sendiri := bacaMetadata(d)[kunciPasal]

		for _, m := range reRujukanPasal.FindAllStringSubmatch(d.Content, -1) {
			n := m[1]
			if n == sendiri {
				continue
			}
			if _, ada := terpakai[n]; ada {
				continue
			}

			terpakai[n] = struct{}{}
			nomor = append(nomor, n)

			if len(nomor) >= maksRujukanSilang {
				return nomor
			}
		}
	}

	return nomor
}

// nomorPasalDirujuk mengumpulkan nomor pasal yang disebut di pertanyaan, tanpa
// pengulangan dan tetap dalam urutan penyebutannya.
func nomorPasalDirujuk(query string) []string {
	var nomor []string
	dilihat := make(map[string]struct{})

	for _, m := range reRujukanPasal.FindAllStringSubmatch(query, -1) {
		if _, ada := dilihat[m[1]]; ada {
			continue
		}
		dilihat[m[1]] = struct{}{}
		nomor = append(nomor, m[1])
	}

	return nomor
}

// filterPasal menyusun filter Qdrant untuk pasal-pasal yang disebut.
//
// Beberapa nomor digabung dengan Should, yang berarti ATAU — pertanyaan seperti
// "apa bedanya pasal 1 dan pasal 5" harus mengambil keduanya, bukan salah satu.
func filterPasal(nomor []string) *qdrantclient.Filter {
	kondisi := make([]*qdrantclient.Condition, 0, len(nomor))
	for _, n := range nomor {
		kondisi = append(kondisi, qdrantclient.NewMatchKeyword(bidangPasal, n))
	}

	return &qdrantclient.Filter{Should: kondisi}
}

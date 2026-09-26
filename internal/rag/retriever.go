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

// reRujukanPasal menangkap rujukan pasal di dalam pertanyaan: "pasal 1",
// "Pasal 139", "pasal 89 ayat (2)".
//
// Hanya nomornya yang diambil. Rujukan ke ayat atau huruf tidak dipakai untuk
// menyaring, karena satu pasal jarang dipecah sampai ke tingkat itu.
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
	asalPasal  = "pasal"
	asalVektor = "vektor"
)

// topKPasal adalah jumlah potongan yang diambil saat menyaring per pasal.
//
// Nilainya jauh di atas kebutuhan: pasal terpanjang di dokumen contoh hanya
// terpecah menjadi 7 potongan. Kelonggaran ini disengaja — yang diambil adalah
// seluruh isi satu pasal, dan kehilangan sebagiannya persis masalah yang
// sedang diperbaiki di sini.
const topKPasal = 20

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
type retrieverPasal struct {
	dasar retriever.Retriever
}

// newRetrieverPasal membungkus retriever dengan penyaringan per pasal.
func newRetrieverPasal(dasar retriever.Retriever) *retrieverPasal {
	return &retrieverPasal{dasar: dasar}
}

// Retrieve menjalankan pencarian vektor, lalu menambahkan seluruh potongan
// pasal yang dirujuk di depan hasilnya.
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

	log.Printf("rag: rujukan pasal %v — %d potongan pasal diletakkan di depan %d hasil vektor",
		nomor, len(pasal), len(vektor))

	return r.gabung(pasal, vektor), nil
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

// gabung menggabungkan hasil pasal dan hasil vektor, dengan hasil pasal lebih
// dulu. Potongan yang muncul di keduanya hanya disimpan sekali.
//
// Asal potongan ditandai di sini, pada saat pertama kali ia terlihat. Menandai
// sebelum penggabungan akan salah untuk potongan yang ditemukan kedua cara:
// penanda yang terakhir menimpanya.
func (r *retrieverPasal) gabung(pasal, vektor []*schema.Document) []*schema.Document {
	hasil := make([]*schema.Document, 0, len(pasal)+len(vektor))
	dilihat := make(map[string]struct{}, len(pasal)+len(vektor))

	for _, kelompok := range []struct {
		docs []*schema.Document
		asal string
	}{
		{pasal, asalPasal},
		{vektor, asalVektor},
	} {
		for _, d := range kelompok.docs {
			if _, ada := dilihat[d.ID]; ada {
				continue
			}
			dilihat[d.ID] = struct{}{}

			if d.MetaData == nil {
				d.MetaData = make(map[string]any, 1)
			}
			d.MetaData[kunciAsal] = kelompok.asal

			hasil = append(hasil, d)
		}
	}

	return hasil
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

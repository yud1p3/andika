package rag

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// kunciPayloadIsi adalah kunci payload tempat indexer Qdrant menyimpan isi
// potongan. Sejajar dengan kunciPayloadMetadata di cari.go; keduanya mengikuti
// nama bawaan indexer (lihat internal/ingest).
const kunciPayloadIsi = "content"

// ukuranHalamanScroll adalah jumlah titik yang diminta per putaran Scroll.
//
// Qdrant membatasi jumlah titik per panggilan, jadi pembacaannya dilakukan
// bertahap lewat offset. Ukurannya cukup besar untuk menuntaskan satu pasal
// dalam satu putaran pada dokumen contoh, tetapi tetap aman untuk pasal lain
// yang potongannya lebih banyak.
const ukuranHalamanScroll = 256

// maksPotonganPasal membatasi berapa potongan yang dikembalikan untuk satu
// pasal.
//
// Dokumen contoh memecah pasal terpanjang menjadi 7 potongan, jadi batas ini
// tidak pernah tercapai di sana. Batas tetap dipasang sebagai jaring pengaman:
// satu nomor pasal yang sama bisa saja dipakai dokumen lain di collection yang
// sama, dan tanpa batas seluruh isinya ikut terkirim ke halaman.
const maksPotonganPasal = 64

// IsiPasal adalah isi lengkap satu pasal yang siap ditampilkan.
type IsiPasal struct {
	// Nomor adalah nomor pasal tanpa kata "Pasal", mis. "7".
	Nomor string
	// Potongan adalah isi pasal, sudah diurutkan sesuai urutan dokumen.
	// Pasal yang muat utuh hanya berisi satu potongan.
	Potongan []string
}

// PembacaPasal mengambil seluruh potongan satu pasal langsung dari Qdrant
// berdasarkan metadatanya.
//
// Berbeda dari jalur retrieval (retriever.go), pembacaan di sini tidak memakai
// kemiripan vektor sama sekali. Yang dicari adalah isi lengkap pasal bernomor
// tertentu, dan untuk itu nomor pasalnya sudah cukup: filter metadata
// mengembalikan tepat potongan-potongan pasal itu, tanpa perlu meng-embed
// query. Konsekuensinya tidak ada panggilan ke Gemini per pembacaan — penting
// karena tombol rujukan bisa diklik berkali-kali.
type PembacaPasal struct {
	qc         *qdrantclient.Client
	collection string
}

// NewPembacaPasal menyiapkan pembaca dari koneksi Qdrant yang sudah ada.
//
// Tidak butuh model embedding: seluruh pekerjaannya membaca payload.
func NewPembacaPasal(qc *qdrantclient.Client, collection string) *PembacaPasal {
	return &PembacaPasal{qc: qc, collection: collection}
}

// Baca mengembalikan seluruh potongan pasal bernomor nomor, sudah terurut.
//
// Pasal yang tidak ada di dokumen menghasilkan IsiPasal dengan Potongan kosong
// dan tanpa error: nomor yang salah adalah masukan pengguna, bukan kegagalan
// sistem, jadi pemanggil cukup menampilkan pesan "tidak ditemukan".
func (p *PembacaPasal) Baca(ctx context.Context, nomor string) (IsiPasal, error) {
	filter := &qdrantclient.Filter{
		Must: []*qdrantclient.Condition{qdrantclient.NewMatchKeyword(bidangPasal, nomor)},
	}

	var (
		potongan []string
		offset   *qdrantclient.PointId
	)

	for {
		req := &qdrantclient.ScrollPoints{
			CollectionName: p.collection,
			Filter:         filter,
			Limit:          qdrantclient.PtrOf(uint32(ukuranHalamanScroll)),
			// Payload dibutuhkan (isinya ada di sana), vektor tidak: ia
			// berukuran besar dan tidak dipakai sama sekali di sini.
			WithPayload: qdrantclient.NewWithPayload(true),
			WithVectors: qdrantclient.NewWithVectors(false),
		}
		if offset != nil {
			req.Offset = offset
		}

		titik, next, err := p.qc.ScrollAndOffset(ctx, req)
		if err != nil {
			return IsiPasal{}, fmt.Errorf("membaca pasal %s dari Qdrant: %w", nomor, err)
		}

		for _, t := range titik {
			if isi := t.GetPayload()[kunciPayloadIsi].GetStringValue(); isi != "" {
				potongan = append(potongan, isi)
			}
		}

		if len(potongan) >= maksPotonganPasal {
			potongan = potongan[:maksPotonganPasal]
			break
		}
		if next == nil {
			break
		}
		offset = next
	}

	urutkanPotongan(potongan)

	return IsiPasal{Nomor: nomor, Potongan: potongan}, nil
}

// Pola header potongan hasil pemecahan, ditulis internal/ingest (lihat
// headerPasal dan potongUnitBesar di pasal.go). Di sini hanya dibaca.
var (
	// reAyatHeader cocok dengan header berayat atau berbutir:
	// "Pasal 7 ayat (2)-(4)", "Pasal 1 angka (1)-(3)".
	reAyatHeader = regexp.MustCompile(`(?:ayat|angka) \((\d+)\)`)
	// reBagianHeader cocok dengan penanda bagian, dalam dua bentuk yang
	// ditulis ingest: "(bagian 2/5)" pada potongan tanpa ayat, dan
	// "ayat (5), bagian 2/5" pada satu ayat yang masih dipecah lagi. Karena
	// itu tanda kurungnya tidak ikut dipatok.
	reBagianHeader = regexp.MustCompile(`bagian (\d+)/\d+`)
)

// urutkanPotongan menyusun ulang potongan pasal ke urutan dokumen.
//
// Qdrant Scroll tidak menjamin urutan apa pun — ia mengembalikan titik menurut
// ID-nya, dan ID potongan diturunkan dari sidik jari isinya (lihat
// assignChunkIDs di internal/ingest), bukan dari posisinya. Tanpa pengurutan
// ini, potongan pasal yang terpecah bisa tampil terbalik-balik.
//
// Urutannya dibaca dari header buatan yang selalu dipasang ingest di awal
// setiap potongan hasil pemecahan. Pasal yang muat utuh tidak berheader dan
// hanya punya satu potongan, jadi tidak ada yang perlu diurutkan.
func urutkanPotongan(potongan []string) {
	sort.SliceStable(potongan, func(i, j int) bool {
		ri, ni, si := kunciUrutan(potongan[i])
		rj, nj, sj := kunciUrutan(potongan[j])

		if ri != rj {
			return ri < rj
		}
		if ni != nj {
			return ni < nj
		}
		return si < sj
	})
}

// kunciUrutan membaca posisi sebuah potongan dari baris pertamanya.
//
// Nilai kembaliannya adalah kunci berurut: rank memisahkan teks pengantar dari
// ayat sehingga keduanya tidak bertabrakan saat nomornya sama, num adalah
// nomor ayat/butir (atau nomor bagian untuk pengantar), dan sub adalah nomor
// bagian ketika satu ayat masih dipecah lagi.
//
// Potongan tunggal tanpa header menghasilkan kunci nol. Itu tidak masalah:
// pasal seperti itu memang hanya punya satu potongan.
func kunciUrutan(isi string) (rank, num, sub int) {
	baris := isi
	if i := strings.IndexByte(baris, '\n'); i >= 0 {
		baris = baris[:i]
	}

	sub = angkaHeader(reBagianHeader, baris)

	if num = angkaHeader(reAyatHeader, baris); num > 0 {
		return 1, num, sub
	}
	if sub > 0 {
		return 0, sub, 0
	}
	return 0, 0, 0
}

// angkaHeader mengambil angka pertama yang ditangkap re dari teks.
func angkaHeader(re *regexp.Regexp, teks string) int {
	m := re.FindStringSubmatch(teks)
	if m == nil {
		return 0
	}

	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

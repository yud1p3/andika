// Berkas ini berisi jalur baca kedua: merekomendasikan kode klasifikasi dari
// perihal naskah yang diketik pengguna dengan format KK#perihal#.
//
// Alurnya sengaja dibuat pendek dan berbeda dari chain RAG perbup:
//
//	embed perihal -> cari kandidat di Qdrant -> serahkan ke model -> jawab
//
// Tidak ada penulisan ulang pertanyaan, memori percakapan, maupun rujukan
// pasal: permintaan klasifikasi berdiri sendiri, dan kandidatnya sudah membawa
// uraian yang dibutuhkan model. Pencarian memakai collection terpisah
// (lihat internal/ingest/kode.go) sehingga tidak pernah menyentuh isi perbup.
package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// Kunci payload titik kode klasifikasi. Ditulis internal/ingest (kode.go);
// di sini hanya dibaca. Kedua sisi harus memakai nama yang sama.
const (
	kunciPayloadKode   = "kode"
	kunciPayloadUraian = "uraian"
	kunciPayloadPath   = "path"
)

// Kandidat adalah satu kode klasifikasi yang mirip dengan perihal yang dicari.
type Kandidat struct {
	Kode   string
	Uraian string
	// Path adalah hierarki lengkap ("A > B > C"). Inilah teks yang di-embed
	// saat vektor dibuat, jadi ia yang paling menggambarkan arti kandidat ini.
	Path string
	// Skor adalah kemiripan cosine dari Qdrant; makin besar makin mirip.
	Skor float32
}

// faktorLebarKandidat adalah pengali topK saat meminta hasil dari Qdrant.
//
// Beberapa baris bisa berbagi kode yang sama (lihat catatan di
// internal/ingest/kode.go). Karena hasil digabung per kode, mengambil tepat
// topK dari Qdrant bisa menyisakan slot kosong setelah penggabungan. Meminta
// lebih banyak dulu lalu memotong setelah digabung menjaga jumlah kandidat
// tetap topK selama datanya cukup.
const faktorLebarKandidat = 3

// PencariKode mencari kode klasifikasi yang mirip dengan sebuah perihal.
//
// Berbeda dari retriever perbup yang memakai retriever Eino, di sini pencarian
// ditulis langsung dengan client Qdrant: payload kode dan uraian ditulis
// sendiri oleh jalur impor, jadi bentuknya sudah pasti dan tidak perlu
// melewati lapisan konversi metadata.
type PencariKode struct {
	qc         *qdrantclient.Client
	collection string
	embed      embedding.Embedder
}

// NewPencariKode menyiapkan pencari dari koneksi Qdrant dan embedder query.
//
// Embedder yang dipakai harus QueryEmbedder (TaskType RETRIEVAL_QUERY), bukan
// DocEmbedder: vektor kode di Qdrant dibuat sebagai dokumen
// (RETRIEVAL_DOCUMENT), dan pasangan query/dokumen itulah yang membuat
// pencarian akurat.
func NewPencariKode(qc *qdrantclient.Client, collection string, embed embedding.Embedder) *PencariKode {
	return &PencariKode{qc: qc, collection: collection, embed: embed}
}

// Cari mengembalikan topK kode klasifikasi yang paling mirip dengan perihal.
func (p *PencariKode) Cari(ctx context.Context, perihal string, topK int) ([]Kandidat, error) {
	if strings.TrimSpace(perihal) == "" {
		return nil, fmt.Errorf("perihal kosong")
	}
	if topK < 1 {
		return nil, fmt.Errorf("jumlah kandidat %d tidak masuk akal; harus minimal 1", topK)
	}

	// Keberadaan collection diperiksa lebih dulu supaya pengguna mendapat pesan
	// "jalankan rag impor-kode", bukan error Qdrant "collection not found".
	ada, err := p.qc.CollectionExists(ctx, p.collection)
	if err != nil {
		return nil, fmt.Errorf("menghubungi Qdrant: %w", err)
	}
	if !ada {
		return nil, fmt.Errorf(
			"collection %q belum ada; jalankan \"rag impor-kode\" untuk menyalin "+
				"kode klasifikasi dari PostgreSQL", p.collection)
	}

	vektor, err := p.embedPerihal(ctx, perihal)
	if err != nil {
		return nil, err
	}

	// Diminta lebih banyak dari topK karena hasil digabung per kode di bawah:
	// baris berkode sama tidak boleh memakan lebih dari satu slot.
	limit := topK * faktorLebarKandidat

	titik, err := p.qc.Query(ctx, &qdrantclient.QueryPoints{
		CollectionName: p.collection,
		Query:          qdrantclient.NewQueryDense(vektor),
		Limit:          qdrantclient.PtrOf(uint64(limit)),
		// Payload dibutuhkan (kode, uraian, path ada di sana), vektor tidak.
		WithPayload: qdrantclient.NewWithPayload(true),
		WithVectors: qdrantclient.NewWithVectors(false),
	})
	if err != nil {
		return nil, fmt.Errorf("mencari kode klasifikasi: %w", err)
	}

	return gabungPerKode(titik, topK), nil
}

// gabungPerKode mengubah titik hasil pencarian menjadi kandidat, dengan
// menggabungkan baris yang berkode sama dan menyimpan yang berskor terbaik.
//
// Qdrant mengembalikan hasil terurut skor menurun, jadi kemunculan pertama
// sebuah kode sudah yang terbaik dan kemunculan berikutnya dilewati. Hasilnya
// dipotong ke maks, sehingga daftar kandidat berisi kode yang berbeda-beda.
func gabungPerKode(titik []*qdrantclient.ScoredPoint, maks int) []Kandidat {
	kandidat := make([]Kandidat, 0, maks)
	dilihat := make(map[string]struct{}, maks)

	for _, t := range titik {
		payload := t.GetPayload()
		kode := payload[kunciPayloadKode].GetStringValue()

		if _, ada := dilihat[kode]; ada {
			continue
		}
		dilihat[kode] = struct{}{}

		kandidat = append(kandidat, Kandidat{
			Kode:   kode,
			Uraian: payload[kunciPayloadUraian].GetStringValue(),
			Path:   payload[kunciPayloadPath].GetStringValue(),
			Skor:   t.GetScore(),
		})

		if len(kandidat) >= maks {
			break
		}
	}

	return kandidat
}

// embedPerihal mengubah teks perihal menjadi vektor query.
func (p *PencariKode) embedPerihal(ctx context.Context, perihal string) ([]float32, error) {
	vecs, err := p.embed.EmbedStrings(ctx, []string{perihal})
	if err != nil {
		return nil, fmt.Errorf("meng-embed perihal: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("embedder mengembalikan vektor kosong untuk perihal %q", perihal)
	}

	vektor := make([]float32, len(vecs[0]))
	for i, v := range vecs[0] {
		vektor[i] = float32(v)
	}
	return vektor, nil
}

// PencariKandidat adalah kontrak pencarian kandidat kode. Dipenuhi *PencariKode.
//
// Dibuat sebagai antarmuka supaya penyusunan rekomendasi bisa diuji tanpa
// Qdrant maupun API key — sama seperti retriever.Retriever di jalur perbup.
type PencariKandidat interface {
	Cari(ctx context.Context, perihal string, topK int) ([]Kandidat, error)
}

// PenjawabKode menyusun rekomendasi kode klasifikasi dari perihal naskah.
type PenjawabKode struct {
	pencari PencariKandidat
	cm      model.ToolCallingChatModel
	topK    int
}

// NewPenjawabKode merakit pencari kandidat dan model yang menyusun rekomendasi.
func NewPenjawabKode(pencari PencariKandidat, cm model.ToolCallingChatModel, topK int) *PenjawabKode {
	return &PenjawabKode{pencari: pencari, cm: cm, topK: topK}
}

// instruksiKode adalah peran tetap model rekomendasi.
//
// Bentuk keluarannya dipatok agar jawabannya seragam dan langsung bisa dibaca:
// kode terbaik, alasan, lalu alternatif. Larangan mengarang kode ditambahkan
// karena model cenderung melengkapi daftar dengan kode yang "kedengaran benar"
// padahal tidak ada di kandidat.
//
// Baris kosong antar-bagian diminta secara eksplisit. Markdown merender satu
// baris baru tunggal sebagai spasi, bukan baris baru — tanpa baris kosong,
// "Kode terbaik", "Alasan", dan "Alternatif" menyatu dalam satu paragraf di
// layar walau di keluaran model tampak terpisah.
const instruksiKode = `Anda membantu memilih kode klasifikasi naskah dinas.
Pengguna memberikan perihal naskah, dan sistem menyertakan beberapa kandidat
kode beserta uraiannya, diurutkan dari yang paling mirip.

Pilih satu kode yang paling sesuai, lalu sebutkan 2-3 alternatif berikutnya.
Jawab dalam bahasa Indonesia dan ringkas, dengan bentuk:

Kode terbaik: <kode> — <uraian>

Alasan: <satu sampai dua kalimat>

Alternatif:
- <kode> — <uraian>
- <kode> — <uraian>

Pisahkan tiap bagian dengan satu baris kosong supaya tampil di baris terpisah.
Jangan mengarang kode yang tidak ada di daftar kandidat.`

// Rekomendasi mengembalikan aliran teks rekomendasi untuk satu perihal.
//
// Aliran dikembalikan, bukan string, supaya client web bisa menampilkannya
// bertahap. Pemanggil wajib memanggil Close pada reader yang dikembalikan.
func (p *PenjawabKode) Rekomendasi(ctx context.Context, perihal string) (*schema.StreamReader[string], error) {
	kandidat, err := p.pencari.Cari(ctx, perihal, p.topK)
	if err != nil {
		return nil, err
	}

	// Tidak ada kandidat: model tidak perlu dipanggil — tidak ada bahan untuk
	// dipilih, dan memanggilnya hanya berisiko menghasilkan kode karangan.
	if len(kandidat) == 0 {
		return aliranSatuPesan(
			"Tidak ada kandidat kode klasifikasi yang cocok untuk perihal ini. " +
				"Coba perihal yang lebih spesifik, atau pastikan data kode sudah diimpor."), nil
	}

	sr, err := p.cm.Stream(ctx, []*schema.Message{
		schema.SystemMessage(instruksiKode),
		schema.UserMessage(pesanKode(perihal, kandidat)),
	})
	if err != nil {
		return nil, fmt.Errorf("meminta rekomendasi kode: %w", err)
	}

	return schema.StreamReaderWithConvert(sr, func(msg *schema.Message) (string, error) {
		if msg == nil {
			return "", fmt.Errorf("model tidak mengembalikan jawaban")
		}
		return msg.Content, nil
	}), nil
}

// pesanKode menyusun pesan user: perihal yang dicari dan daftar kandidat.
//
// Tiap kandidat ditampilkan dalam tiga baris: kode dan uraian, path hierarki
// lengkap, lalu skor. Path disertakan karena itulah teks yang di-embed saat
// vektor dibuat (lihat internal/ingest/kode.go), jadi ia paling menggambarkan
// arti kode — uraian sering terlalu umum ("Sistem informasi") tanpa leluhurnya.
//
// Skor tetap dicantumkan supaya model tahu urutan keyakinan hasil pencarian;
// ia tetap yang memutuskan.
func pesanKode(perihal string, kandidat []Kandidat) string {
	var b strings.Builder

	b.WriteString("perihal naskah: ")
	b.WriteString(strings.TrimSpace(perihal))
	b.WriteString("\n\nkandidat kode (paling mirip lebih dulu):")

	for _, k := range kandidat {
		b.WriteString("\n- ")
		b.WriteString(k.Kode)
		b.WriteString(" — ")
		b.WriteString(k.Uraian)
		b.WriteString("\n  path: ")
		b.WriteString(k.Path)
		b.WriteString(fmt.Sprintf("\n  skor: %.3f", k.Skor))
	}

	return b.String()
}

// aliranSatuPesan membungkus satu teks menjadi aliran, supaya pemanggil hanya
// perlu menangani satu jenis nilai balik.
func aliranSatuPesan(teks string) *schema.StreamReader[string] {
	out, tulis := schema.Pipe[string](1)

	go func() {
		defer tulis.Close()
		tulis.Send(teks, nil)
	}()

	return out
}

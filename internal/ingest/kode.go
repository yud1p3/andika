// Berkas ini berisi jalur tulis kedua: menyalin kode klasifikasi beserta
// vektornya dari PostgreSQL ke Qdrant.
//
// Berbeda dari Run (ingest.go) yang meng-embed dokumen dengan Gemini, di sini
// vektor sudah jadi: ia dibuat di luar proyek ini dengan model yang sama
// (Gemini, dimensi sama). Karena itu vektornya disalin apa adanya — tidak ada
// panggilan embedding sama sekali, sehingga impor tidak memakan kuota.
//
// Syarat sahihnya salinan mentah ini: model, dimensi, dan TaskType vektor di
// PostgreSQL harus sama dengan yang dipakai proyek ini (gemini-embedding-2,
// GEMINI_EMBED_DIM, RETRIEVAL_DOCUMENT). Dimensi yang berbeda ditolak di sini;
// model/TaskType yang berbeda tidak bisa dideteksi otomatis, jadi syarat itu
// dicatat di README dan .env.example.
package ingest

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
)

// Kunci payload titik kode klasifikasi.
//
// internal/rag (klasifikasi.go) membaca ulang kedua kunci ini saat mencari
// kandidat; keduanya harus memakai nama yang sama persis. Sama seperti
// kunciPayloadIsi di rag, namanya diduplikasi sebagai konstanta dengan
// komentar silang alih-alih dibagi lewat paket perantara.
const (
	kunciPayloadKode   = "kode"
	kunciPayloadUraian = "uraian"
	kunciPayloadPath   = "path"
)

// ukuranBatchKode membatasi berapa titik dikirim per satu panggilan Upsert.
//
// Seluruh kode klasifikasi biasanya hanya ratusan sampai ribuan baris, jadi
// nilainya tidak kritis; batas ini hanya menjaga permintaan tetap wajar dan
// memberi progres yang terlihat saat jumlahnya besar.
const ukuranBatchKode = 256

// barisKode adalah satu baris kode klasifikasi yang sudah lolos validasi,
// siap dijadikan titik Qdrant.
type barisKode struct {
	// ID adalah nilai kolom kunci unik di PostgreSQL. Dipakai sebagai ID titik
	// Qdrant supaya tiap baris punya tempat sendiri walau kodenya sama.
	ID     int64
	Kode   string
	Uraian string
	Path   string
	Vektor []float32
}

// ImporKode membaca kode klasifikasi dari PostgreSQL dan menyimpannya ke
// collection Qdrant khusus, lalu selesai. Aman dijalankan berulang: ID titik
// diturunkan dari kode, jadi impor ulang menimpa titik yang sama, bukan
// menggandakannya.
func ImporKode(ctx context.Context, cfg *config.Config, qc *qdrantclient.Client) error {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return fmt.Errorf(
			"DATABASE_URL belum diisi, jadi kode klasifikasi tidak bisa dibaca. " +
				"Isi string koneksi PostgreSQL di file .env (lihat .env.example)")
	}

	if err := siapkanCollectionKode(ctx, cfg, qc); err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("membuka koneksi PostgreSQL: %w", err)
	}
	defer pool.Close()

	// Ping lebih dulu supaya kegagalan koneksi (host salah, kata sandi keliru)
	// muncul sebagai pesan yang jelas, bukan sebagai error saat query.
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("menghubungi PostgreSQL: %w", err)
	}

	query := fmt.Sprintf(
		"SELECT %s, %s, COALESCE(%s, ''), COALESCE(%s, ''), %s::text FROM %s",
		kutipIdentifier(cfg.KodeKolomID),
		kutipIdentifier(cfg.KodeKolomKode),
		kutipIdentifier(cfg.KodeKolomUraian),
		kutipIdentifier(cfg.KodeKolomPath),
		kutipIdentifier(cfg.KodeKolomVektor),
		kutipIdentifier(cfg.KodeTabel),
	)

	rows, err := pool.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("membaca %s: %w", cfg.KodeTabel, err)
	}
	defer rows.Close()

	log.Printf("membaca kode klasifikasi dari tabel %q...", cfg.KodeTabel)

	var (
		batch = make([]*qdrantclient.PointStruct, 0, ukuranBatchKode)
		total int
	)

	for rows.Next() {
		var (
			id               int64
			kode, uraian     string
			path, vektorTeks string
		)
		if err := rows.Scan(&id, &kode, &uraian, &path, &vektorTeks); err != nil {
			return fmt.Errorf("membaca baris kode klasifikasi: %w", err)
		}

		kode = strings.TrimSpace(kode)
		if kode == "" {
			return fmt.Errorf("ada baris (id %d) dengan %s kosong di tabel %q",
				id, cfg.KodeKolomKode, cfg.KodeTabel)
		}

		vektor, err := parseVektorPg(vektorTeks)
		if err != nil {
			return fmt.Errorf("kode %q (id %d): %w", kode, id, err)
		}
		if len(vektor) != int(cfg.EmbedDim) {
			return fmt.Errorf(
				"kode %q (id %d): vektor berdimensi %d, sedangkan GEMINI_EMBED_DIM=%d. "+
					"Vektor dengan dimensi berbeda tidak bisa dibandingkan; "+
					"pastikan kolom %s dibuat dengan model embedding yang sama",
				kode, id, len(vektor), cfg.EmbedDim, cfg.KodeKolomVektor)
		}

		batch = append(batch, titikKode(barisKode{
			ID:     id,
			Kode:   kode,
			Uraian: uraian,
			Path:   path,
			Vektor: vektor,
		}))
		total++

		if len(batch) >= ukuranBatchKode {
			if err := upsertKode(ctx, cfg, qc, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("membaca %s: %w", cfg.KodeTabel, err)
	}

	if len(batch) > 0 {
		if err := upsertKode(ctx, cfg, qc, batch); err != nil {
			return err
		}
	}

	if total == 0 {
		return fmt.Errorf("tabel %q tidak berisi baris apa pun", cfg.KodeTabel)
	}

	log.Printf("selesai: %d kode klasifikasi tersimpan di collection %q",
		total, cfg.KodeCollection)
	return nil
}

// titikKode mengubah satu baris kode menjadi titik Qdrant.
//
// ID titik memakai kolom kunci unik apa adanya (id), sehingga impor ulang
// menimpa titik yang sama dan baris berkode sama tetap punya tempat sendiri.
func titikKode(b barisKode) *qdrantclient.PointStruct {
	return &qdrantclient.PointStruct{
		Id:      qdrantclient.NewIDNum(uint64(b.ID)),
		Vectors: qdrantclient.NewVectorsDense(b.Vektor),
		Payload: map[string]*qdrantclient.Value{
			kunciPayloadKode:   qdrantclient.NewValueString(b.Kode),
			kunciPayloadUraian: qdrantclient.NewValueString(b.Uraian),
			kunciPayloadPath:   qdrantclient.NewValueString(b.Path),
		},
	}
}

// siapkanCollectionKode memastikan collection kode siap dipakai.
//
// Dibuat dengan dimensi dan metrik jarak yang sama seperti yang dipakai saat
// membuat vektor di PostgreSQL (cosine). Bila collection sudah ada dengan
// dimensi berbeda, impor dihentikan dengan pesan yang menjelaskan — meng-upsert
// vektor berdimensi lain akan gagal dengan error Qdrant yang membingungkan.
func siapkanCollectionKode(ctx context.Context, cfg *config.Config, qc *qdrantclient.Client) error {
	ada, err := qc.CollectionExists(ctx, cfg.KodeCollection)
	if err != nil {
		return fmt.Errorf(
			"tidak bisa menghubungi Qdrant di %s:%d: %w (pastikan server Qdrant sudah jalan)",
			cfg.QdrantHost, cfg.QdrantPort, err)
	}

	if ada {
		info, err := qc.GetCollectionInfo(ctx, cfg.KodeCollection)
		if err != nil {
			return fmt.Errorf("membaca info collection %q: %w", cfg.KodeCollection, err)
		}

		dim := info.GetConfig().GetParams().GetVectorsConfig().GetParams().GetSize()
		if int(dim) != int(cfg.EmbedDim) {
			return fmt.Errorf(
				"collection %q dibuat dengan dimensi %d, sedangkan GEMINI_EMBED_DIM=%d. "+
					"Hapus collection itu (curl -X DELETE %s:%d/collections/%s) lalu jalankan impor lagi",
				cfg.KodeCollection, dim, cfg.EmbedDim,
				cfg.QdrantHost, cfg.QdrantPort, cfg.KodeCollection)
		}

		log.Printf("collection %q sudah ada: %d poin, dimensi %d",
			cfg.KodeCollection, info.GetPointsCount(), dim)
		return nil
	}

	log.Printf("collection %q belum ada, dibuat dengan dimensi %d (cosine)",
		cfg.KodeCollection, cfg.EmbedDim)

	return qc.CreateCollection(ctx, &qdrantclient.CreateCollection{
		CollectionName: cfg.KodeCollection,
		VectorsConfig: qdrantclient.NewVectorsConfig(&qdrantclient.VectorParams{
			Size:     uint64(cfg.EmbedDim),
			Distance: qdrantclient.Distance_Cosine,
		}),
	})
}

// upsertKode mengirim satu batch titik ke Qdrant dan menunggu penerapannya.
//
// Wait=true penting di sini: tanpa itu perintah impor bisa selesai dan keluar
// sebelum Qdrant benar-benar menyimpan datanya, sehingga pengguna yang langsung
// mencoba KK#perihal# akan menemukan collection yang masih kosong.
func upsertKode(ctx context.Context, cfg *config.Config, qc *qdrantclient.Client, batch []*qdrantclient.PointStruct) error {
	if _, err := qc.Upsert(ctx, &qdrantclient.UpsertPoints{
		CollectionName: cfg.KodeCollection,
		Wait:           qdrantclient.PtrOf(true),
		Points:         batch,
	}); err != nil {
		return fmt.Errorf("menyimpan %d kode ke Qdrant: %w", len(batch), err)
	}
	return nil
}

// parseVektorPg mengubah teks vektor pgvector menjadi []float32.
//
// pgvector mengeluarkan vektor sebagai teks berkurung siku — "[0.1,0.2,0.3]" —
// sehingga bentuk inilah yang dibaca lewat "::text" di query. Sengaja tidak
// memakai tipe vektor asli pgx: itu menuntut registrasi tipe ekstensi pgvector,
// yang bisa berbeda antar-versi dan membuat impor gagal karena hal yang tidak
// berhubungan dengan data.
func parseVektorPg(teks string) ([]float32, error) {
	s := strings.TrimSpace(teks)

	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, fmt.Errorf("format vektor tidak dikenal: %q", potongTeks(s))
	}

	isi := strings.TrimSpace(s[1 : len(s)-1])
	if isi == "" {
		return nil, nil
	}

	bagian := strings.Split(isi, ",")
	vektor := make([]float32, 0, len(bagian))

	for _, b := range bagian {
		nilai, err := strconv.ParseFloat(strings.TrimSpace(b), 32)
		if err != nil {
			return nil, fmt.Errorf("angka vektor %q tidak sah: %w", potongTeks(b), err)
		}
		vektor = append(vektor, float32(nilai))
	}

	return vektor, nil
}

// potongTeks memendekkan teks untuk pesan error supaya vektor yang panjang
// tidak membanjiri log.
func potongTeks(s string) string {
	const maks = 40
	if len(s) <= maks {
		return s
	}
	return s[:maks] + "…"
}

// kutipIdentifier membungkus nama tabel/kolom dengan tanda kutip ganda.
//
// Nilainya sudah divalidasi di config.Load sebagai identifier sederhana, jadi
// tanda kutipnya aman: yang diizinkan hanya huruf, angka, dan garis bawah.
func kutipIdentifier(nama string) string {
	return `"` + nama + `"`
}

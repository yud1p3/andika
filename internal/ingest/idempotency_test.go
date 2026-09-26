package ingest

import (
	"context"
	"os"
	"testing"

	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
)

// TestIDHasilGenerateCocokDenganYangTersimpan membuktikan ingest bersifat
// idempotent TANPA memanggil API embedding sama sekali.
//
// Caranya: hasilkan ulang ID dari dokumen sumber yang sama — hanya baca dan
// potong, tidak ada panggilan jaringan — lalu bandingkan dengan ID yang sudah
// tersimpan di Qdrant. Kalau semuanya cocok, menjalankan ingest ulang akan
// mengenali chunk itu sebagai sudah ada dan melewatinya, bukan menambah salinan.
//
// Dibandingkan menjalankan ingest dua kali penuh, cara ini tidak menghabiskan
// kuota embedding dan selesai dalam hitungan detik.
//
// Test dilewati kalau dokumen sumber belum ada atau Qdrant tidak berjalan.
func TestIDHasilGenerateCocokDenganYangTersimpan(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("konfigurasi tidak terbaca: %v", err)
	}

	if _, err := os.Stat(cfg.DocPath); err != nil {
		t.Skipf("dokumen %s belum ada; jalankan 'rag ingest' dulu", cfg.DocPath)
	}

	ctx := context.Background()

	docs, err := loadDoc(ctx, cfg)
	if err != nil {
		t.Fatalf("membaca dokumen: %v", err)
	}

	chunks, err := splitDocs(ctx, cfg, docs)
	if err != nil {
		t.Fatalf("memotong dokumen: %v", err)
	}

	assignChunkIDs(chunks)

	dihasilkan := make(map[string]struct{}, len(chunks))
	for _, c := range chunks {
		dihasilkan[c.ID] = struct{}{}
	}

	qc, err := newTestQdrantClient(cfg)
	if err != nil {
		t.Skipf("Qdrant tidak bisa dihubungi: %v", err)
	}
	defer qc.Close()

	// Idempotensi hanya bisa diuji kalau isi collection memang berasal dari
	// pipeline yang sekarang. Kalau strategi atau ukuran chunk sudah diubah,
	// ID-nya memang berbeda semua — itu data basi, bukan bug idempotensi, dan
	// yang dibutuhkan ingest ulang, bukan perbaikan kode.
	tersimpanSidik, adaSidik, err := sidikJariTersimpan(ctx, cfg, qc)
	if err != nil {
		t.Skipf("membaca sidik jari collection: %v", err)
	}
	sekarang := sidikJariPemotongan(cfg)

	switch {
	case !adaSidik:
		t.Skipf(
			"collection %q belum punya metadata %q, jadi dibuat sebelum penanda ini ada. "+
				"Jalankan 'rag ingest' (atau hapus collection) dulu untuk membangunnya ulang",
			cfg.QdrantCollection, metaSidikJari)
	case tersimpanSidik != sekarang:
		t.Skipf(
			"collection %q dibangun dengan pemotongan %q, sedangkan konfigurasi sekarang %q. "+
				"ID chunk diturunkan dari isinya, jadi keduanya memang tidak akan cocok. "+
				"Jalankan 'rag ingest' ulang — tanpa itu, hasil pencarian masih memakai potongan lama",
			cfg.QdrantCollection, tersimpanSidik, sekarang)
	}

	tersimpan, err := existingChunkIDs(ctx, cfg, qc)
	if err != nil {
		t.Skipf("membaca collection: %v", err)
	}
	if len(tersimpan) == 0 {
		t.Skipf("collection %q masih kosong", cfg.QdrantCollection)
	}

	t.Logf("ID dihasilkan dari dokumen: %d chunk, %d ID unik | poin tersimpan di Qdrant: %d",
		len(chunks), len(dihasilkan), len(tersimpan))
	if len(dihasilkan) < len(chunks) {
		t.Logf("catatan: %d chunk berisi teks kembar, jadi ID-nya menyatu (lihat assignChunkIDs)",
			len(chunks)-len(dihasilkan))
	}

	var tidakCocok []string
	for id := range tersimpan {
		if _, ok := dihasilkan[id]; !ok {
			tidakCocok = append(tidakCocok, id)
		}
	}

	if len(tidakCocok) > 0 {
		contoh := tidakCocok
		if len(contoh) > 3 {
			contoh = contoh[:3]
		}
		t.Errorf(
			"%d dari %d poin tersimpan tidak cocok dengan ID hasil generate (contoh: %v). "+
				"Ingest TIDAK idempotent — menjalankan ulang akan menambah salinan, bukan menimpa.",
			len(tidakCocok), len(tersimpan), contoh)
	}

	// Kesimpulan yang sebenarnya diuji: ingest ulang tidak akan meng-embed
	// apa pun, karena semua chunk dikenali sebagai sudah ada.
	//
	// Pembandingnya adalah jumlah ID unik, bukan jumlah chunk: chunk dengan
	// teks kembar berbagi satu ID, sehingga jumlah titik di Qdrant bisa lebih
	// sedikit daripada jumlah chunk.
	baru := filterNewChunks(chunks, tersimpan)
	mauBaru := len(dihasilkan) - len(tersimpan)
	if len(baru) != mauBaru {
		t.Errorf("filterNewChunks menyisakan %d chunk, mau %d (ID unik %d, tersimpan %d)",
			len(baru), mauBaru, len(dihasilkan), len(tersimpan))
	}
	if len(baru) > 0 {
		t.Logf("catatan: %d chunk belum tersimpan, jadi ingest ulang masih perlu meng-embed sebanyak itu",
			len(baru))
	}
}

// TestFilterNewChunks memastikan penyaringan chunk yang sudah tersimpan
// berperilaku benar, termasuk urutan hasil yang tetap dan kasus batas.
func TestFilterNewChunks(t *testing.T) {
	buat := func(ids ...string) []*schema.Document {
		docs := make([]*schema.Document, 0, len(ids))
		for _, id := range ids {
			docs = append(docs, &schema.Document{ID: id, Content: "isi " + id})
		}
		return docs
	}

	t.Run("sebagian sudah ada", func(t *testing.T) {
		chunks := buat("a", "b", "c", "d")
		sudahAda := map[string]struct{}{"b": {}, "d": {}}

		baru := filterNewChunks(chunks, sudahAda)

		if len(baru) != 2 || baru[0].ID != "a" || baru[1].ID != "c" {
			t.Errorf("dapat %v, mau [a c]", ids(baru))
		}
	})

	t.Run("semua sudah ada", func(t *testing.T) {
		chunks := buat("a", "b")
		sudahAda := map[string]struct{}{"a": {}, "b": {}}

		if baru := filterNewChunks(chunks, sudahAda); len(baru) != 0 {
			t.Errorf("dapat %v, mau kosong", ids(baru))
		}
	})

	t.Run("collection kosong", func(t *testing.T) {
		chunks := buat("a", "b")

		if baru := filterNewChunks(chunks, map[string]struct{}{}); len(baru) != 2 {
			t.Errorf("dapat %v, mau [a b]", ids(baru))
		}
	})

	t.Run("tanpa chunk", func(t *testing.T) {
		if baru := filterNewChunks(nil, map[string]struct{}{"a": {}}); len(baru) != 0 {
			t.Errorf("dapat %v, mau kosong", ids(baru))
		}
	})
}

func ids(docs []*schema.Document) []string {
	out := make([]string, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	return out
}

// newTestQdrantClient membuat client Qdrant dari konfigurasi.
func newTestQdrantClient(cfg *config.Config) (*qdrantclient.Client, error) {
	return qdrantclient.NewClient(&qdrantclient.Config{
		Host: cfg.QdrantHost,
		Port: cfg.QdrantPort,
	})
}

// sidikJariTersimpan membaca sidik jari pemotongan dari satu poin di Qdrant.
//
// Cukup satu poin: seluruh isi collection ditulis oleh satu kali ingest, jadi
// sidik jarinya seragam. Mengembalikan ok=false kalau collection masih kosong
// atau poinnya belum punya metadata itu.
func sidikJariTersimpan(ctx context.Context, cfg *config.Config, qc *qdrantclient.Client) (string, bool, error) {
	points, _, err := qc.ScrollAndOffset(ctx, &qdrantclient.ScrollPoints{
		CollectionName: cfg.QdrantCollection,
		Limit:          qdrantclient.PtrOf(uint32(1)),
		WithPayload:    qdrantclient.NewWithPayload(true),
		WithVectors:    qdrantclient.NewWithVectors(false),
	})
	if err != nil {
		return "", false, err
	}
	if len(points) == 0 {
		return "", false, nil
	}

	// Indexer Qdrant menyimpan doc.MetaData di bawah kunci payload "metadata",
	// bukan sebagai kunci tingkat atas.
	nilai := points[0].GetPayload()["metadata"].GetStructValue().GetFields()[metaSidikJari]
	if nilai == nil {
		return "", false, nil
	}
	return nilai.GetStringValue(), true, nil
}

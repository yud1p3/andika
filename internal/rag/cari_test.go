package rag

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// retrieverPencatat mengembalikan dokumen tetap sambil mencatat query dan
// topK yang diterimanya.
//
// topK perlu dicatat terpisah: ia diteruskan sebagai opsi, bukan sebagai
// argumen, jadi tidak terlihat dari tanda tangan Retrieve.
type retrieverPencatat struct {
	docs    []*schema.Document
	queries []string
	topK    []int
}

// topKAcuan adalah nilai TopK pada base options, dipakai untuk membedakan
// "tidak ada opsi topK" dari "opsi topK berisi nilai lain".
const topKAcuan = 10

func (r *retrieverPencatat) Retrieve(_ context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	r.queries = append(r.queries, query)

	acuan := topKAcuan
	co := retriever.GetCommonOptions(&retriever.Options{TopK: &acuan}, opts...)
	r.topK = append(r.topK, *co.TopK)

	return r.docs, nil
}

// dokSkor merakit dokumen hasil retrieval lengkap dengan skor dan metadata.
func dokSkor(t *testing.T, isi string, skor float64, meta map[string]any) *schema.Document {
	t.Helper()

	doc := &schema.Document{ID: isi, Content: isi}
	if meta != nil {
		doc.MetaData = map[string]any{kunciPayloadMetadata: qdrantclient.NewValueMap(meta)}
	}

	// WithScore dipanggil setelah MetaData diisi: ia menulis skor ke MetaData,
	// dan akan membuat map kosong kalau belum ada.
	return doc.WithScore(skor)
}

// chainPencatat merakit Chain dengan retriever palsu dan model palsu, supaya
// Cari bisa diuji tanpa Qdrant maupun API key.
func chainPencatat(t *testing.T, ret retriever.Retriever) (*Chain, *fakeChatModel) {
	t.Helper()

	cm := &fakeChatModel{}
	chain := newTestChain(t, cm, ret)
	return chain, cm
}

// TestCariMemakaiPertanyaanApaAdanya mengunci dua hal yang membedakan Cari dari
// Ask.
//
// Pertama, pertanyaan dikirim ke retriever tanpa ditulis ulang — perintah ini
// tidak punya riwayat, dan menulis ulang justru akan mengaburkan apa yang
// benar-benar dicari. Kedua, model tidak dipanggil sama sekali: kalau ia
// dipanggil, perintah ini bukan lagi alat diagnosis retrieval.
func TestCariMemakaiPertanyaanApaAdanya(t *testing.T) {
	ret := &retrieverPencatat{docs: []*schema.Document{{ID: "d1", Content: "isi"}}}
	chain, cm := chainPencatat(t, ret)

	const query = "pasal 1 peraturan ini membahas apa"

	if _, err := chain.Cari(context.Background(), query, 0); err != nil {
		t.Fatalf("Cari() gagal: %v", err)
	}

	if len(ret.queries) != 1 || ret.queries[0] != query {
		t.Errorf("query yang diterima retriever = %v, mau [%q]", ret.queries, query)
	}
	if n := len(cm.panggilan); n != 0 {
		t.Errorf("model dipanggil %d kali, mau 0", n)
	}
}

// TestCariMeneruskanTopK memastikan topK hanya dikirim kalau memang diminta.
//
// topK 0 berarti "pakai TOP_K dari konfigurasi", jadi opsinya harus absen —
// bukan dikirim sebagai nol, yang akan meminta nol potongan.
func TestCariMeneruskanTopK(t *testing.T) {
	for nama, kasus := range map[string]struct {
		topK int
		mau  int
	}{
		"nol berarti pakai konfigurasi": {topK: 0, mau: topKAcuan},
		"negatif diperlakukan sama":     {topK: -3, mau: topKAcuan},
		"nilai positif diteruskan":      {topK: 25, mau: 25},
	} {
		t.Run(nama, func(t *testing.T) {
			ret := &retrieverPencatat{}
			chain, _ := chainPencatat(t, ret)

			if _, err := chain.Cari(context.Background(), "q", kasus.topK); err != nil {
				t.Fatalf("Cari() gagal: %v", err)
			}

			if len(ret.topK) != 1 || ret.topK[0] != kasus.mau {
				t.Errorf("topK yang diterima retriever = %v, mau [%d]", ret.topK, kasus.mau)
			}
		})
	}
}

// TestCariMemberiPeringkatDanSkor memastikan hasilnya bisa dibaca sebagai
// daftar berperingkat.
//
// Peringkat dimulai dari 1 — bukan 0 — karena yang membacanya manusia, dan
// itulah yang dipakai saat membandingkan dengan urutan hasil di Qdrant.
func TestCariMemberiPeringkatDanSkor(t *testing.T) {
	ret := &retrieverPencatat{docs: []*schema.Document{
		dokSkor(t, "Pasal 1", 0.7312, map[string]any{"pasal": "1"}),
		dokSkor(t, "Pasal 9", 0.7204, map[string]any{"pasal": "9"}),
		dokSkor(t, "Pasal 10", 0.7101, map[string]any{"pasal": "10"}),
	}}
	chain, _ := chainPencatat(t, ret)

	temuan, err := chain.Cari(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("Cari() gagal: %v", err)
	}

	if len(temuan) != 3 {
		t.Fatalf("Cari() mengembalikan %d temuan, mau 3", len(temuan))
	}

	for i, tt := range temuan {
		if tt.Peringkat != i+1 {
			t.Errorf("temuan %d: peringkat = %d, mau %d", i, tt.Peringkat, i+1)
		}
		if tt.Isi != ret.docs[i].Content {
			t.Errorf("temuan %d: isi = %q, mau %q", i, tt.Isi, ret.docs[i].Content)
		}
		if tt.Skor != ret.docs[i].Score() {
			t.Errorf("temuan %d: skor = %v, mau %v", i, tt.Skor, ret.docs[i].Score())
		}
		asal := ret.docs[i].MetaData[kunciPayloadMetadata].(map[string]*qdrantclient.Value)
		if tt.Meta["pasal"] != nilaiKeString(asal["pasal"]) {
			t.Errorf("temuan %d: metadata pasal = %q, tidak sesuai dokumen asalnya", i, tt.Meta["pasal"])
		}
	}
}

// TestCariTanpaHasil menjaga collection yang tidak mengembalikan apa pun.
//
// Ini keadaan yang wajar — collection kosong, atau filter terlalu ketat — dan
// harus menghasilkan daftar kosong, bukan panic.
func TestCariTanpaHasil(t *testing.T) {
	chain, _ := chainPencatat(t, &retrieverPencatat{})

	temuan, err := chain.Cari(context.Background(), "q", 0)
	if err != nil {
		t.Fatalf("Cari() gagal: %v", err)
	}
	if len(temuan) != 0 {
		t.Errorf("Cari() mengembalikan %d temuan, mau 0", len(temuan))
	}
}

// TestChainMenyimpanRetriever menjaga seam yang dipakai Cari.
//
// newDenganRetriever menerima retriever sebagai argumen; kalau ia lupa
// menyimpannya ke Chain, Cari akan memanggil nil dan panik saat dijalankan —
// bukan saat dirakit, sehingga baru ketahuan di pemakaian sungguhan.
func TestChainMenyimpanRetriever(t *testing.T) {
	ret := &retrieverPencatat{}
	chain, _ := chainPencatat(t, ret)

	if chain.ret == nil {
		t.Fatal("Chain.ret nil; Cari akan panik saat dipanggil")
	}

	// Dipastikan benar-benar retriever yang sama, bukan retriever lain.
	if _, err := chain.Cari(context.Background(), "q", 0); err != nil {
		t.Fatalf("Cari() gagal: %v", err)
	}
	if len(ret.queries) != 1 {
		t.Errorf("retriever yang disimpan tidak dipanggil (queries=%v)", ret.queries)
	}
}

// payloadUji menirukan bentuk payload yang dikembalikan retriever Qdrant:
// metadata dikumpulkan di bawah satu kunci "metadata", bukan sebagai kunci
// tingkat atas.
func payloadUji(t *testing.T, isi map[string]any) *schema.Document {
	t.Helper()

	return &schema.Document{
		Content: "Pasal 1 ...",
		MetaData: map[string]any{
			kunciPayloadMetadata: qdrantclient.NewValueMap(isi),
		},
	}
}

func TestBacaMetadata(t *testing.T) {
	doc := payloadUji(t, map[string]any{
		"bagian": "batang_tubuh",
		"pasal":  "1",
		"ayat":   "1-5",
	})

	got := bacaMetadata(doc)

	mau := map[string]string{
		"bagian": "batang_tubuh",
		"pasal":  "1",
		"ayat":   "1-5",
	}
	if len(got) != len(mau) {
		t.Fatalf("bacaMetadata() = %v, mau %v", got, mau)
	}
	for k, v := range mau {
		if got[k] != v {
			t.Errorf("metadata %q = %q, mau %q", k, got[k], v)
		}
	}
}

// TestNilaiKeStringMenurutJenis menjaga konversi tiap jenis nilai payload.
//
// Metadata dari splitter selalu string, tetapi perintah "rag cari" dipakai
// untuk memeriksa apa yang benar-benar tersimpan. Kalau nilai bertipe lain
// ditampilkan kosong, justru hal yang sedang dicari yang hilang.
//
// Nilai ujinya dibuat lewat qdrant.NewValueMap — fungsi yang sama yang dipakai
// indexer Qdrant saat menyimpan. Merakit *qdrant.Value dengan tangan akan
// menguji bentuk yang belum tentu sama dengan yang benar-benar tersimpan;
// persis di situ kesalahan tipe bisa lolos dari test.
func TestNilaiKeStringMenurutJenis(t *testing.T) {
	vm := qdrantclient.NewValueMap(map[string]any{
		"teks":    "batang_tubuh",
		"bulat":   int64(7),
		"pecahan": 2.5,
		"benar":   true,
		"kosong":  nil,
	})

	mau := map[string]string{
		"teks":    "batang_tubuh",
		"bulat":   "7",
		"pecahan": "2.5",
		"benar":   "true",
		"kosong":  "",
	}

	for k, v := range mau {
		t.Run(k, func(t *testing.T) {
			if got := nilaiKeString(vm[k]); got != v {
				t.Errorf("nilaiKeString(%s) = %q, mau %q", k, got, v)
			}
		})
	}
}

// TestNilaiKeStringNilaiBertingkat menjaga struct dan list tidak menghilang.
//
// Bentuk protobuf-nya tidak dipatok persis karena itu detail internal
// pustaka; yang penting isinya masih terbaca, bukan string kosong.
func TestNilaiKeStringNilaiBertingkat(t *testing.T) {
	vm := qdrantclient.NewValueMap(map[string]any{
		"daftar":    []any{"a", "b"},
		"bersarang": map[string]any{"pasal": "1"},
	})

	for nama, v := range vm {
		t.Run(nama, func(t *testing.T) {
			got := nilaiKeString(v)
			if got == "" {
				t.Fatalf("nilaiKeString() kosong; nilai bertingkat menghilang dari keluaran")
			}
			if !strings.Contains(got, "a") && !strings.Contains(got, "pasal") {
				t.Errorf("nilaiKeString() = %q, tidak memuat isi nilainya", got)
			}
		})
	}
}

// TestBacaMetadataTanpaPayload menjaga dua bentuk dokumen yang tidak punya
// metadata sama sekali.
//
// Retriever selalu mengisi kunci "metadata" dengan map kosong, tetapi dokumen
// yang dirakit langsung di test bisa saja tidak punya map itu. Keduanya harus
// menghasilkan nil, bukan panic.
func TestBacaMetadataTanpaPayload(t *testing.T) {
	t.Run("metadata nil", func(t *testing.T) {
		if got := bacaMetadata(&schema.Document{Content: "isi"}); got != nil {
			t.Errorf("bacaMetadata() = %v, mau nil", got)
		}
	})

	t.Run("kunci metadata tidak ada", func(t *testing.T) {
		doc := &schema.Document{
			Content:  "isi",
			MetaData: map[string]any{"kunci_lain": "nilai"},
		}
		if got := bacaMetadata(doc); got != nil {
			t.Errorf("bacaMetadata() = %v, mau nil", got)
		}
	})

	t.Run("metadata kosong", func(t *testing.T) {
		doc := payloadUji(t, map[string]any{})

		got := bacaMetadata(doc)

		if got == nil {
			t.Fatal("bacaMetadata() = nil, mau map kosong (bukan nil)")
		}
		if len(got) != 0 {
			t.Errorf("bacaMetadata() = %v, mau kosong", got)
		}
	})
}

// Package ingest menjalankan jalur tulis (write path) pipeline RAG: mengunduh
// dokumen sumber, memotongnya menjadi bagian-bagian kecil, mengubah setiap
// bagian menjadi vector, lalu menyimpannya ke Qdrant.
//
// Ini adalah padanan sel-sel notebook mulai dari "Load Dokumen PDF" sampai
// "Vector Database — ChromaDB".
package ingest

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	fileloader "github.com/cloudwego/eino-ext/components/document/loader/file"
	pdfparser "github.com/cloudwego/eino-ext/components/document/parser/pdf"
	"github.com/cloudwego/eino-ext/components/document/transformer/splitter/recursive"
	qdrantindexer "github.com/cloudwego/eino-ext/components/indexer/qdrant"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/document/parser"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
	"rag-eino/internal/gemini"
)

// Run menjalankan seluruh jalur tulis sampai chunk tersimpan di Qdrant.
//
// Urutannya: siapkan dokumen -> baca -> potong jadi chunk -> beri ID unik ->
// cek collection -> lewati chunk yang sudah ada -> embed dan simpan sisanya.
// Fungsi ini dijalankan sekali saja; setelah data tersimpan, perintah "ask"
// bisa dipakai berulang kali tanpa mengulang proses embedding.
//
// Ingest bersifat inkremental: karena ID chunk diturunkan dari isinya, chunk
// yang sudah tersimpan bisa dikenali dan dilewati. Jadi menjalankan ulang
// setelah kegagalan di tengah hanya meng-embed sisa yang belum masuk, bukan
// mengulang seluruh dokumen. Ini penting karena kuota embedding Gemini dihitung
// per teks, sehingga mengulang ratusan chunk yang sudah benar itu boros.
func Run(ctx context.Context, cfg *config.Config, ai *gemini.Client, qc *qdrantclient.Client) error {
	if err := ensureDoc(ctx, cfg); err != nil {
		return fmt.Errorf("menyiapkan dokumen sumber: %w", err)
	}

	docs, err := loadDoc(ctx, cfg)
	if err != nil {
		return err
	}
	log.Printf("dokumen dibaca: %d bagian dari %s", len(docs), filepath.Base(cfg.DocPath))

	chunks, err := splitDocs(ctx, cfg, docs)
	if err != nil {
		return err
	}
	log.Printf("chunking selesai: %d potongan (chunk_size=%d, overlap=%d)",
		len(chunks), cfg.ChunkSize, cfg.ChunkOverlap)

	assignChunkIDs(chunks)

	if err := checkCollection(ctx, cfg, qc); err != nil {
		return err
	}

	sudahAda, err := existingChunkIDs(ctx, cfg, qc)
	if err != nil {
		return err
	}

	baru := filterNewChunks(chunks, sudahAda)
	if dilewati := len(chunks) - len(baru); dilewati > 0 {
		log.Printf("ingest inkremental: %d chunk sudah ada dan dilewati, %d chunk perlu di-embed",
			dilewati, len(baru))
	}

	if len(baru) == 0 {
		log.Printf("selesai: semua %d chunk sudah tersimpan di collection %q, tidak ada yang perlu di-embed",
			len(chunks), cfg.QdrantCollection)
		return nil
	}

	if err := indexChunks(ctx, cfg, ai, qc, baru); err != nil {
		return err
	}

	log.Printf("selesai: %d chunk tersimpan di collection %q (dokumen berisi %d chunk)",
		len(baru), cfg.QdrantCollection, len(chunks))
	return nil
}

// ensureDoc menyiapkan dokumen sumber di disk.
//
// Kalau berkasnya sudah ada, tidak ada yang dilakukan. Kalau belum ada dan
// DocURL diisi, dokumennya diunduh — ini padanan perintah
// `!curl -o ai_pv.pdf <url>` di notebook. Kalau DocURL dikosongkan, dokumen
// dianggap memang disiapkan lokal (misalnya hasil konversi dengan xberg) dan
// ketidakhadirannya dilaporkan sebagai error yang jelas, bukan kegagalan
// unduhan yang membingungkan.
func ensureDoc(ctx context.Context, cfg *config.Config) error {
	if _, err := os.Stat(cfg.DocPath); err == nil {
		log.Printf("dokumen sudah ada di %s, unduhan dilewati", cfg.DocPath)
		return nil
	}

	if cfg.DocURL == "" {
		return fmt.Errorf(
			"dokumen %s tidak ditemukan dan DOC_URL kosong, jadi tidak ada yang bisa diunduh",
			cfg.DocPath)
	}

	log.Printf("mengunduh dokumen dari %s", cfg.DocURL)

	if err := os.MkdirAll(filepath.Dir(cfg.DocPath), 0o755); err != nil {
		return fmt.Errorf("membuat direktori %s: %w", filepath.Dir(cfg.DocPath), err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.DocURL, nil)
	if err != nil {
		return err
	}
	// Sebagian situs menolak request tanpa User-Agent.
	req.Header.Set("User-Agent", "rag-eino/1.0")

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mengunduh %s: %w", cfg.DocURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mengunduh %s: server menjawab %s", cfg.DocURL, resp.Status)
	}

	// Unduh ke file sementara dulu, baru dipindahkan. Kalau unduhan gagal di
	// tengah jalan, tidak ada file rusak yang tertinggal dan dianggap sudah
	// lengkap pada percobaan berikutnya.
	tmp := cfg.DocPath + ".tmp"
	n, err := downloadToFile(resp.Body, tmp)
	if err != nil {
		return fmt.Errorf("mengunduh %s: %w", cfg.DocURL, err)
	}
	if err := os.Rename(tmp, cfg.DocPath); err != nil {
		os.Remove(tmp)
		return err
	}

	log.Printf("dokumen tersimpan: %s (%.1f KB)", cfg.DocPath, float64(n)/1024)
	return nil
}

// downloadToFile menulis isi r ke path dan mengembalikan jumlah byte tertulis.
func downloadToFile(r io.Reader, path string) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}

	n, err := io.Copy(f, r)
	if err != nil {
		f.Close()
		os.Remove(path)
		return 0, err
	}

	if err := f.Close(); err != nil {
		os.Remove(path)
		return 0, err
	}

	return n, nil
}

// loadDoc membaca dokumen sumber menjadi satu atau lebih Document.
//
// Parser dipilih berdasarkan ekstensi berkas. Parser PDF didaftarkan khusus
// untuk ".pdf" supaya PDF dibaca per halaman; ekstensi lain — termasuk Markdown
// hasil konversi dengan xberg — jatuh ke TextParser bawaan Eino dan dibaca
// sebagai teks apa adanya. Karena itu mengganti dokumen sumber dari PDF ke
// Markdown cukup dengan mengubah DOC_PATH, tanpa mengubah kode.
//
// Padanan PyPDFLoader.load_and_split() di notebook: ToPages=true membuat parser
// PDF memecah hasilnya per halaman, bukan menggabungkan seluruh dokumen menjadi
// satu teks panjang.
func loadDoc(ctx context.Context, cfg *config.Config) ([]*schema.Document, error) {
	pdfParser, err := pdfparser.NewPDFParser(ctx, &pdfparser.Config{ToPages: true})
	if err != nil {
		return nil, fmt.Errorf("membuat PDF parser: %w", err)
	}

	extParser, err := parser.NewExtParser(ctx, &parser.ExtParserConfig{
		Parsers: map[string]parser.Parser{".pdf": pdfParser},
	})
	if err != nil {
		return nil, fmt.Errorf("membuat parser per ekstensi: %w", err)
	}

	loader, err := fileloader.NewFileLoader(ctx, &fileloader.FileLoaderConfig{Parser: extParser})
	if err != nil {
		return nil, fmt.Errorf("membuat file loader: %w", err)
	}

	docs, err := loader.Load(ctx, document.Source{URI: cfg.DocPath})
	if err != nil {
		return nil, fmt.Errorf("membaca %s: %w", cfg.DocPath, err)
	}

	return docs, nil
}

// recursiveSplitter membuat splitter berbasis ukuran, sama seperti notebook.
//
// Pemisah defaultnya adalah ["\n", ".", "?", "!"], jadi ia memotong pada batas
// kalimat tanpa menghiraukan struktur dokumen. Itu sebabnya strategi "pasal"
// tidak memakainya untuk batang tubuh, hanya sebagai cadangan.
func recursiveSplitter(ctx context.Context, cfg *config.Config) (document.Transformer, error) {
	splitter, err := recursive.NewSplitter(ctx, &recursive.Config{
		ChunkSize:   cfg.ChunkSize,
		OverlapSize: cfg.ChunkOverlap,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat splitter: %w", err)
	}
	return splitter, nil
}

// splitDocs memotong dokumen menjadi potongan-potongan kecil, lalu membuang
// potongan yang terlalu kecil untuk berguna.
//
// Ini satu-satunya pintu masuk pemotongan. Penyaringan degenerat sengaja
// dikerjakan di dalam sini, bukan di Run, supaya jalur yang dipakai test
// idempotent (splitDocs lalu assignChunkIDs) menghasilkan ID yang sama persis
// dengan jalur ingest sungguhan. Penandaan sidik jari pemotongan juga di sini,
// dengan alasan yang sama.
func splitDocs(ctx context.Context, cfg *config.Config, docs []*schema.Document) ([]*schema.Document, error) {
	var (
		chunks []*schema.Document
		err    error
	)

	switch cfg.ChunkStrategy {
	case "recursive":
		chunks, err = potongRecursive(ctx, cfg, docs)
	case "pasal":
		chunks, err = potongPerPasal(ctx, cfg, docs)
	default:
		// config.Load sudah menolak nilai yang tidak dikenal, jadi ini hanya
		// jaring pengaman kalau Config dirakit langsung oleh test.
		return nil, fmt.Errorf("strategi pemotongan %q tidak dikenal", cfg.ChunkStrategy)
	}
	if err != nil {
		return nil, err
	}

	tandaiSidikJari(chunks, cfg)

	return saringDegenerat(chunks), nil
}

// metaSidikJari adalah kunci metadata yang mencatat parameter pemotongan.
const metaSidikJari = "sidik_jari"

// sidikJariPemotongan merangkum parameter yang menentukan isi dan batas chunk.
//
// Diikutkan ke metadata setiap chunk supaya data yang tersimpan bisa dikenali
// asalnya. Gunanya bukan sekadar dokumentasi: ID chunk diturunkan dari isinya,
// jadi mengubah strategi atau ukuran chunk mengubah semua ID, dan data lama di
// Qdrant berhenti tertimpa. Tanpa penanda ini, keadaan itu terlihat sama
// dengan bug idempotensi — padahal keduanya butuh tindakan yang berbeda.
//
// Hanya parameter pemotongan yang masuk, bukan isi dokumen: dokumen sumber
// diganti seluruhnya berarti ingest ulang penuh, dan itu memang di luar cakupan
// penanda ini.
func sidikJariPemotongan(cfg *config.Config) string {
	return fmt.Sprintf("%s/%d/%d", cfg.ChunkStrategy, cfg.ChunkSize, cfg.ChunkOverlap)
}

// tandaiSidikJari menuliskan sidik jari pemotongan ke metadata setiap chunk.
//
// Metadata chunk dari splitter pasal selalu ada, tetapi chunk dari splitter
// cadangan belum tentu, jadi map-nya disiapkan lebih dulu.
func tandaiSidikJari(chunks []*schema.Document, cfg *config.Config) {
	sidik := sidikJariPemotongan(cfg)
	for _, c := range chunks {
		if c.MetaData == nil {
			c.MetaData = make(map[string]any, 1)
		}
		c.MetaData[metaSidikJari] = sidik
	}
}

// potongRecursive memotong dokumen berdasarkan ukuran saja, tanpa menghiraukan
// struktur. Ini cara notebook.
func potongRecursive(ctx context.Context, cfg *config.Config, docs []*schema.Document) ([]*schema.Document, error) {
	splitter, err := recursiveSplitter(ctx, cfg)
	if err != nil {
		return nil, err
	}

	chunks, err := splitter.Transform(ctx, docs)
	if err != nil {
		return nil, fmt.Errorf("memotong dokumen: %w", err)
	}

	return chunks, nil
}

// Ambang saringan chunk degenerat.
const (
	// minChunkChars adalah panjang minimum sebuah chunk.
	minChunkChars = 80
	// minChunkAlnum adalah jumlah minimum karakter alfanumerik dalam sebuah
	// chunk.
	minChunkAlnum = 40
)

// saringDegenerat membuang chunk yang isinya terlalu sedikit untuk berguna.
//
// Alasannya terukur, bukan teoretis. Sebelum saringan ini ada, salah satu chunk
// panjangnya 3 karakter, dan hasil pencarian untuk "pasal 1" menempatkan
// template kop surat berisi titik-titik di peringkat 2 — di atas pasal
// sungguhan. Teks yang nyaris tanpa huruf menghasilkan vektor yang hampir
// degenerate, dan vektor seperti itu bisa kebetulan berskor tinggi terhadap
// pertanyaan apa pun. Chunk seperti itu hanya memakan slot TOP_K.
//
// Chunk dengan isi kembar juga dibuang. Selain tidak menambah informasi, ID-nya
// toh menyatu di assignChunkIDs — jadi membiarkannya hanya membuat embedder
// dipanggil dua kali untuk teks yang sama.
func saringDegenerat(chunks []*schema.Document) []*schema.Document {
	hasil := make([]*schema.Document, 0, len(chunks))
	dilihat := make(map[string]struct{}, len(chunks))

	var pendek, kembar int

	for _, c := range chunks {
		isi := strings.TrimSpace(c.Content)

		if _, ada := dilihat[isi]; ada {
			kembar++
			continue
		}
		dilihat[isi] = struct{}{}

		if len(isi) < minChunkChars || jumlahAlnum(isi) < minChunkAlnum {
			pendek++
			continue
		}

		hasil = append(hasil, c)
	}

	if pendek > 0 || kembar > 0 {
		log.Printf(
			"saringan chunk: %d dibuang karena terlalu pendek atau nyaris tanpa huruf, "+
				"%d dibuang karena isinya kembar (%d -> %d chunk)",
			pendek, kembar, len(chunks), len(hasil))
	}

	return hasil
}

// jumlahAlnum menghitung karakter alfanumerik dalam teks.
func jumlahAlnum(teks string) int {
	n := 0
	for _, r := range teks {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// chunkIDNamespace adalah namespace tetap untuk menurunkan ID chunk.
//
// Nilainya tidak penting selama tidak pernah berubah: mengubahnya akan
// mengubah semua ID, sehingga data lama di Qdrant tidak lagi tertimpa dan
// malah menumpuk.
var chunkIDNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("rag-eino/chunk"))

// assignChunkIDs memberi setiap chunk ID deterministik yang diturunkan dari
// isinya.
//
// Ada dua alasan langkah ini wajib:
//
//  1. Parser dokumen tidak mengisi ID dokumen, sehingga semua chunk ID-nya
//     kosong. Indexer Qdrant memakai doc.ID sebagai point ID, dan Qdrant hanya
//     menerima UUID atau bilangan bulat. Tanpa ID, penyimpanan gagal.
//
//  2. ID yang deterministik membuat ingest idempotent. Menjalankan ingest
//     berkali-kali untuk dokumen yang sama menghasilkan ID yang sama, sehingga
//     Qdrant menimpa (upsert) titik yang sudah ada alih-alih menambah salinan
//     baru. Dengan ID acak, satu kali gagal di tengah lalu dijalankan ulang
//     akan menggandakan seluruh data.
//
// Konsekuensinya, dua chunk dengan isi identik dianggap satu titik. Untuk
// sumber pengetahuan RAG hal ini justru diinginkan, karena teks kembar tidak
// menambah informasi apa pun.
func assignChunkIDs(chunks []*schema.Document) {
	for _, c := range chunks {
		c.ID = uuid.NewSHA1(chunkIDNamespace, []byte(c.Content)).String()
	}
}

// checkCollection memastikan collection siap dipakai sebelum indexing.
//
// Collection yang belum ada akan dibuat otomatis oleh indexer Qdrant dengan
// dimensi dan metrik jarak dari config. Kalau collection sudah ada tapi
// dimensinya berbeda (misalnya setelah GEMINI_EMBED_DIM diganti), proses
// upsert akan gagal dengan pesan yang membingungkan, jadi kasus itu dideteksi
// lebih awal di sini.
func checkCollection(ctx context.Context, cfg *config.Config, qc *qdrantclient.Client) error {
	exists, err := qc.CollectionExists(ctx, cfg.QdrantCollection)
	if err != nil {
		return fmt.Errorf(
			"tidak bisa menghubungi Qdrant di %s:%d: %w (pastikan server Qdrant sudah jalan)",
			cfg.QdrantHost, cfg.QdrantPort, err)
	}

	if !exists {
		log.Printf("collection %q belum ada, akan dibuat otomatis saat penyimpanan", cfg.QdrantCollection)
		return nil
	}

	info, err := qc.GetCollectionInfo(ctx, cfg.QdrantCollection)
	if err != nil {
		return fmt.Errorf("membaca info collection %q: %w", cfg.QdrantCollection, err)
	}

	dim := info.GetConfig().GetParams().GetVectorsConfig().GetParams().GetSize()
	if int(dim) != int(cfg.EmbedDim) {
		return fmt.Errorf(
			"collection %q dibuat dengan dimensi %d, sedangkan GEMINI_EMBED_DIM=%d. "+
				"Vector dengan dimensi berbeda tidak bisa dibandingkan. "+
				"Hapus collection lama (curl -X DELETE %s:%d/collections/%s) lalu jalankan ingest lagi",
			cfg.QdrantCollection, dim, cfg.EmbedDim,
			cfg.QdrantHost, cfg.QdrantPort, cfg.QdrantCollection)
	}

	log.Printf("collection %q sudah ada: %d poin, dimensi %d",
		cfg.QdrantCollection, info.GetPointsCount(), dim)

	return nil
}

// existingChunkIDs mengumpulkan seluruh point ID yang sudah ada di collection.
//
// Ini yang membuat ingest inkremental mungkin: ID chunk diturunkan dari isi
// chunk, jadi ID yang sudah ada berarti isi yang sama sudah tersimpan dan
// tidak perlu di-embed ulang.
//
// Pengambilan dilakukan bertahap karena Qdrant membatasi jumlah titik yang
// dikembalikan dalam satu kali panggil. Payload dan vector sengaja tidak
// diminta supaya responsnya ringan — yang dibutuhkan hanya ID-nya.
func existingChunkIDs(ctx context.Context, cfg *config.Config, qc *qdrantclient.Client) (map[string]struct{}, error) {
	exists, err := qc.CollectionExists(ctx, cfg.QdrantCollection)
	if err != nil {
		return nil, fmt.Errorf("memeriksa collection %q: %w", cfg.QdrantCollection, err)
	}
	if !exists {
		// Collection belum dibuat; belum ada apa pun yang tersimpan.
		return map[string]struct{}{}, nil
	}

	ada := make(map[string]struct{})

	var offset *qdrantclient.PointId
	for {
		req := &qdrantclient.ScrollPoints{
			CollectionName: cfg.QdrantCollection,
			Limit:          qdrantclient.PtrOf(uint32(256)),
			WithPayload:    qdrantclient.NewWithPayload(false),
			WithVectors:    qdrantclient.NewWithVectors(false),
		}
		if offset != nil {
			req.Offset = offset
		}

		points, next, err := qc.ScrollAndOffset(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("membaca ID yang tersimpan di collection %q: %w",
				cfg.QdrantCollection, err)
		}

		for _, p := range points {
			if id := p.GetId().GetUuid(); id != "" {
				ada[id] = struct{}{}
			}
		}

		if next == nil {
			return ada, nil
		}
		offset = next
	}
}

// filterNewChunks mengembalikan hanya chunk yang ID-nya belum ada di Qdrant.
func filterNewChunks(chunks []*schema.Document, sudahAda map[string]struct{}) []*schema.Document {
	baru := make([]*schema.Document, 0, len(chunks))
	for _, c := range chunks {
		if _, ada := sudahAda[c.ID]; ada {
			continue
		}
		baru = append(baru, c)
	}
	return baru
}

// indexChunks meng-embed setiap chunk lalu menyimpannya ke Qdrant.
//
// Padanan Chroma.from_documents() di notebook: embedding dan penyimpanan
// terjadi sekaligus, kita tidak perlu memanggil embedder secara manual.
func indexChunks(ctx context.Context, cfg *config.Config, ai *gemini.Client, qc *qdrantclient.Client, chunks []*schema.Document) error {
	idx, err := qdrantindexer.NewIndexer(ctx, &qdrantindexer.Config{
		Client:     qc,
		Collection: cfg.QdrantCollection,
		VectorDim:  int(cfg.EmbedDim),
		Distance:   qdrantclient.Distance_Cosine,
		Embedding:  ai.DocEmbedder,
	})
	if err != nil {
		return fmt.Errorf("membuat indexer Qdrant: %w", err)
	}

	log.Printf("meng-embed dan menyimpan %d chunk (butuh beberapa saat)...", len(chunks))

	if _, err := idx.Store(ctx, chunks); err != nil {
		return fmt.Errorf("menyimpan chunk ke Qdrant: %w", err)
	}

	return nil
}

// Command rag adalah CLI untuk pipeline RAG (Retrieval-Augmented Generation)
// yang dibangun dengan Eino dan Gemini, memakai Qdrant sebagai vector database.
//
// Ini adalah versi Go dari notebook pelatihan
// Copy_of_RAG_Gemini_AVPN_IT_Data.ipynb yang ditulis dengan Python, LangChain,
// dan ChromaDB.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/cloudwego/eino-ext/components/document/transformer/splitter/recursive"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
	"rag-eino/internal/gemini"
	"rag-eino/internal/ingest"
	"rag-eino/internal/rag"
	"rag-eino/internal/web"
)

const usage = `rag — RAG (Retrieval-Augmented Generation) dengan Gemini, Eino, dan Qdrant

Pemakaian:
  rag ingest       Unduh dokumen, potong jadi chunk, embed, lalu simpan ke Qdrant
                   (jalankan sekali saja sebelum perintah "ask")
  rag ask          Tanya-jawab interaktif berdasarkan dokumen yang tersimpan
  rag cari         Tampilkan potongan yang diambil retriever untuk sebuah pertanyaan
                   (tanpa memanggil model; untuk memeriksa hasil pencarian)
  rag serve        Client web untuk tanya-jawab (default http://127.0.0.1:8080)
  rag chunk-demo   Peragakan cara kerja chunking pada teks pendek (tanpa API key)

Opsi "rag cari":
  -topk int        Jumlah potongan yang diambil (default: TOP_K dari konfigurasi)
  -penuh           Tampilkan isi potongan selengkapnya, bukan ringkasannya

Opsi "rag serve":
  -addr string     Alamat dan port yang didengarkan (default "127.0.0.1:8080")

Perintah di dalam "rag ask":
  reset            Kosongkan riwayat percakapan, mulai lagi dari konteks kosong
  exit             Keluar

Konfigurasi dibaca dari file .env. Lihat .env.example untuk daftar lengkapnya;
minimal GEMINI_API_KEY harus diisi.
`

func main() {
	// Tanpa prefix tanggal supaya keluaran progres mudah dibaca.
	log.SetFlags(0)

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	// chunk-demo sengaja dijalankan sebelum memuat konfigurasi supaya bisa
	// dicoba tanpa API key dan tanpa Qdrant.
	if os.Args[1] == "chunk-demo" {
		if err := runChunkDemo(); err != nil {
			log.Fatalf("chunk-demo gagal: %v", err)
		}
		return
	}

	// Ctrl-C membatalkan context, sehingga request yang sedang berjalan ikut
	// berhenti dengan rapi.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("konfigurasi tidak valid: %v", err)
	}

	switch os.Args[1] {
	case "ingest":
		err = runIngest(ctx, cfg)
	case "ask":
		err = runAsk(ctx, cfg)
	case "cari":
		err = runCari(ctx, cfg, os.Args[2:])
	case "serve":
		err = runServe(ctx, cfg, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "perintah %q tidak dikenal\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		log.Fatalf("%s gagal: %v", os.Args[1], err)
	}
}

// runIngest menjalankan jalur tulis: unduh, potong, embed, simpan ke Qdrant.
func runIngest(ctx context.Context, cfg *config.Config) error {
	ai, err := gemini.New(ctx, cfg)
	if err != nil {
		return err
	}

	qc, err := newQdrantClient(cfg)
	if err != nil {
		return err
	}
	defer qc.Close()

	return ingest.Run(ctx, cfg, ai, qc)
}

// runAsk menjalankan jalur baca: tanya-jawab interaktif di terminal.
func runAsk(ctx context.Context, cfg *config.Config) error {
	ai, err := gemini.New(ctx, cfg)
	if err != nil {
		return err
	}

	qc, err := newQdrantClient(cfg)
	if err != nil {
		return err
	}
	defer qc.Close()

	chain, err := rag.New(ctx, cfg, ai, qc)
	if err != nil {
		return err
	}

	fmt.Printf("Model: %s | Collection: %s | top-k: %d\n",
		cfg.ChatModel, cfg.QdrantCollection, cfg.TopK)
	fmt.Println("Ketik pertanyaan lalu Enter. Keluar dengan 'exit', Ctrl-D, atau Ctrl-C.")
	if cfg.HistoryMessages > 0 {
		fmt.Printf("Riwayat %d pesan terakhir dipakai untuk memahami rujukan. Ketik 'reset' untuk mengosongkannya.\n",
			cfg.HistoryMessages)
	} else {
		fmt.Println("Memori percakapan mati (HISTORY_MESSAGES=0); tiap pertanyaan dijawab lepas.")
	}

	// Jawaban model berformat Markdown. Ditata sekali di awal supaya tidak
	// memeriksa TTY berulang kali untuk tiap pertanyaan.
	berGaya := stdoutBergaya()

	// Riwayat percakapan disimpan di sini, bukan di dalam Chain. Dengan begitu
	// satu Chain tetap tak berstatus dan bisa melayani banyak percakapan
	// sekaligus — yang memang dilakukan "rag serve" untuk tiap sesi.
	var riwayat []*schema.Message

	scanner := bufio.NewScanner(os.Stdin)
	// Pertanyaan panjang tidak terpotong di batas default 64 KB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Masukan dibaca di goroutine terpisah dan diambil lewat channel, bukan
	// langsung dari goroutine ini.
	//
	// Alasannya: scanner.Scan() yang menunggu ketikan user memblokir
	// goroutine-nya dan tidak bereaksi terhadap context yang dibatalkan. Kalau
	// dibaca langsung di sini, Ctrl-C hanya membatalkan context tapi program
	// tetap tersangkut menunggu Enter. Dengan channel, Ctrl-C langsung
	// menghentikan program.
	lines := readLines(ctx, scanner)

	for {
		fmt.Print("\n> ")

		var in line
		select {
		case <-ctx.Done():
			// Ctrl-C.
			fmt.Println()
			return nil
		case v, ok := <-lines:
			if !ok {
				// Ctrl-D: stdin sudah habis.
				fmt.Println()
				return nil
			}
			in = v
		}

		if in.err != nil {
			return fmt.Errorf("membaca input: %w", in.err)
		}

		question := strings.TrimSpace(in.text)
		switch question {
		case "":
			continue
		case "exit", "quit":
			return nil
		case "reset", "baru":
			riwayat = nil
			fmt.Println("Riwayat percakapan dikosongkan.")
			continue
		}

		// Rujukan ditampung lewat callback, bukan dikembalikan Chain.Ask:
		// potongan yang terpakai baru diketahui setelah node retrieval
		// berjalan di tengah graph, sedangkan Ask hanya mengembalikan teks
		// jawabannya.
		var sumber []string

		answer, err := chain.Ask(ctx, rag.Pertanyaan{
			Teks: question,
			// Dipangkas tepat sebelum dikirim, bukan saat disimpan, supaya
			// mengubah HISTORY_MESSAGES langsung terasa tanpa memulai ulang
			// percakapan.
			Riwayat:  rag.PangkasRiwayat(riwayat, cfg.HistoryMessages),
			OnSumber: func(label []string) { sumber = label },
		})
		if err != nil {
			// Satu pertanyaan gagal tidak perlu menghentikan seluruh sesi.
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("pertanyaan gagal dijawab: %v", err)
			continue
		}

		fmt.Printf("\n%s\n", renderMarkdown(answer, berGaya))

		// Dicetak setelah jawaban, bukan sebelumnya: di terminal tidak ada
		// tempat menaruh rujukan di samping jawaban, dan mendahuluinya dengan
		// daftar panjang justru menunda yang dicari pengguna.
		if len(sumber) > 0 {
			fmt.Printf("\nRujukan: %s\n", strings.Join(sumber, ", "))
		}

		// Dicatat hanya setelah jawabannya berhasil. Pertanyaan yang gagal
		// tidak ikut jadi konteks pertanyaan berikutnya: pengguna tidak pernah
		// melihat jawabannya, jadi ia tidak akan menganggapnya ada.
		riwayat = append(riwayat,
			schema.UserMessage(question),
			schema.AssistantMessage(answer, nil),
		)
	}
}

// runCari menampilkan potongan dokumen yang diambil retriever untuk sebuah
// pertanyaan, tanpa memanggil model sama sekali.
//
// Perintah ini ada karena jawaban akhir yang salah tidak memberi tahu di mana
// masalahnya: bisa potongan yang dibutuhkan tidak terambil, bisa juga terambil
// tapi disimpulkan keliru. Dengan melihat peringkat, skor, dan metadata tiap
// potongan, keduanya bisa dibedakan — dan itulah yang menentukan apakah yang
// perlu diperbaiki chunking, TOP_K, atau promptnya.
func runCari(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("cari", flag.ContinueOnError)
	topK := fs.Int("topk", 0, "jumlah potongan yang diambil (0 = pakai TOP_K dari konfigurasi)")
	penuh := fs.Bool("penuh", false, "tampilkan isi potongan selengkapnya")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Pertanyaan diambil dari argumen yang tersisa, bukan dari satu flag,
	// supaya tidak perlu dikutip: rag cari pasal 1 membahas apa
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return errors.New(`pertanyaan belum diisi; contoh: rag cari "pasal 1 membahas apa"`)
	}

	ai, err := gemini.New(ctx, cfg)
	if err != nil {
		return err
	}

	qc, err := newQdrantClient(cfg)
	if err != nil {
		return err
	}
	defer qc.Close()

	chain, err := rag.New(ctx, cfg, ai, qc)
	if err != nil {
		return err
	}

	// Pertanyaan dipakai apa adanya: tidak ditulis ulang lewat riwayat, karena
	// perintah ini tidak punya riwayat percakapan.
	temuan, err := chain.Cari(ctx, query, *topK)
	if err != nil {
		return err
	}

	jumlahTopK := *topK
	if jumlahTopK <= 0 {
		jumlahTopK = cfg.TopK
	}

	fmt.Printf("Pertanyaan : %s\n", query)
	fmt.Printf("Collection : %s | top-k: %d\n\n", cfg.QdrantCollection, jumlahTopK)

	if len(temuan) == 0 {
		fmt.Println("Tidak ada potongan yang dikembalikan retriever.")
		return nil
	}

	for _, t := range temuan {
		fmt.Printf("#%d  skor %.4f", t.Peringkat, t.Skor)
		if t.Asal != "" {
			// Asal ditampilkan karena ia menjelaskan mengapa potongan ini ikut
			// terambil. Potongan berlabel "pasal" ada di situ bukan karena
			// mirip secara vektor, melainkan karena nomor pasalnya disebut di
			// pertanyaan — jadi skornya yang rendah bukan tanda keliru.
			fmt.Printf("  [%s]", t.Asal)
		}
		fmt.Println()

		if len(t.Meta) > 0 {
			fmt.Printf("    %s\n", barisMetadata(t.Meta))
		}

		if *penuh {
			for _, baris := range strings.Split(strings.TrimSpace(t.Isi), "\n") {
				fmt.Printf("    %s\n", baris)
			}
		} else {
			fmt.Printf("    %s\n", ringkas(t.Isi, 200))
		}
		fmt.Println()
	}

	return nil
}

// barisMetadata menyusun metadata potongan menjadi satu baris "kunci=nilai".
//
// Kuncinya diurutkan supaya keluarannya bisa dibandingkan antar-potongan dan
// antar-jalankan; urutan map Go sengaja diacak, jadi tanpa pengurutan baris ini
// berubah-ubah setiap kali perintah dijalankan.
func barisMetadata(meta map[string]string) string {
	kunci := make([]string, 0, len(meta))
	for k := range meta {
		kunci = append(kunci, k)
	}
	sort.Strings(kunci)

	bagian := make([]string, 0, len(kunci))
	for _, k := range kunci {
		bagian = append(bagian, k+"="+meta[k])
	}

	return strings.Join(bagian, " ")
}

// ringkas memampatkan isi potongan menjadi satu baris sepanjang maks karakter.
//
// Spasi beruntun dan baris baru dirapatkan supaya satu potongan tetap terbaca
// dalam satu baris, sehingga beberapa potongan bisa dibandingkan sekaligus di
// layar.
func ringkas(isi string, maks int) string {
	rapat := strings.Join(strings.Fields(isi), " ")

	// Dipotong per rune, bukan per byte, supaya karakter beraksen tidak
	// terbelah menjadi byte rusak.
	r := []rune(rapat)
	if len(r) <= maks {
		return rapat
	}
	return string(r[:maks]) + "…"
}

// runServe menjalankan client web untuk tanya-jawab.
func runServe(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "alamat dan port yang didengarkan")

	if err := fs.Parse(args); err != nil {
		return err
	}

	ai, err := gemini.New(ctx, cfg)
	if err != nil {
		return err
	}

	qc, err := newQdrantClient(cfg)
	if err != nil {
		return err
	}
	defer qc.Close()

	chain, err := rag.New(ctx, cfg, ai, qc)
	if err != nil {
		return err
	}

	srv, err := web.New(alirkanDariChain(chain, cfg.HistoryMessages), web.Tampilan{
		Title:      cfg.AppTitle,
		Subtitle:   cfg.AppSubtitle,
		Model:      cfg.ChatModel,
		Collection: cfg.QdrantCollection,
		TopK:       cfg.TopK,
	})
	if err != nil {
		return err
	}

	return srv.Serve(ctx, *addr)
}

// alirkanDariChain menyambungkan Chain.Stream ke tipe web.Alirkan.
//
// Chain.Stream mengembalikan aliran potongan teks; setiap potongan diteruskan
// ke delta supaya bisa ditampilkan bertahap di browser.
//
// Rujukan potongan disalurkan lewat sumber. Callback itu dipasang di
// rag.Pertanyaan, bukan dipanggil setelah aliran habis, karena chain baru tahu
// potongan mana yang terpakai setelah node retrieval berjalan — dan itu terjadi
// di tengah aliran, sebelum potongan jawaban pertama tiba.
//
// Fungsi ini sekaligus satu-satunya tempat paket web bertemu tipe Eino:
// riwayat diterjemahkan dari web.Pesan ke schema.Message sebelum masuk chain.
func alirkanDariChain(chain *rag.Chain, historyMaks int) web.Alirkan {
	return func(ctx context.Context, pertanyaan string, riwayat []web.Pesan,
		delta func(string) error, sumber func([]string) error) error {
		sr, err := chain.Stream(ctx, rag.Pertanyaan{
			Teks:    pertanyaan,
			Riwayat: rag.PangkasRiwayat(keSchemaMessages(riwayat), historyMaks),
			// Kesalahan dari sumber diabaikan di sini: yang dilaporkan ke
			// pemanggil cukup satu error, dan kegagalan mengirim rujukan —
			// biasanya karena klien memutus koneksi — akan muncul lagi pada
			// delta pertama.
			OnSumber: func(label []string) {
				if err := sumber(label); err != nil {
					log.Printf("rag: mengirim rujukan gagal: %v", err)
				}
			},
		})
		if err != nil {
			return err
		}
		defer sr.Close()

		for {
			potongan, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := delta(potongan); err != nil {
				return err
			}
		}
	}
}

// keSchemaMessages menerjemahkan riwayat percakapan dari tipe netral paket web
// ke tipe pesan Eino.
func keSchemaMessages(riwayat []web.Pesan) []*schema.Message {
	if len(riwayat) == 0 {
		return nil
	}

	msgs := make([]*schema.Message, 0, len(riwayat))
	for _, p := range riwayat {
		if p.Peran == web.PeranAssistant {
			msgs = append(msgs, schema.AssistantMessage(p.Isi, nil))
			continue
		}
		msgs = append(msgs, schema.UserMessage(p.Isi))
	}
	return msgs
}

// line adalah satu baris masukan dari stdin, atau error yang muncul saat
// membacanya.
type line struct {
	text string
	err  error
}

// readLines membaca stdin di goroutine terpisah lalu mengirim setiap baris ke
// channel yang dikembalikan. Channel ditutup begitu stdin habis (Ctrl-D).
func readLines(ctx context.Context, scanner *bufio.Scanner) <-chan line {
	lines := make(chan line)

	go func() {
		defer close(lines)

		for scanner.Scan() {
			select {
			case lines <- line{text: scanner.Text()}:
			case <-ctx.Done():
				return
			}
		}

		if err := scanner.Err(); err != nil {
			// select dipakai supaya goroutine ini tidak menggantung kalau
			// program sudah lebih dulu keluar dan tidak ada yang membaca.
			select {
			case lines <- line{err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return lines
}

// runChunkDemo memperagakan cara kerja chunking pada teks pendek, sama seperti
// sel "Demo Chunking" di notebook.
//
// Perintah ini tidak memanggil API apa pun, jadi cocok dijalankan lebih dulu
// untuk melihat bagaimana sebuah teks dipotong sebelum memproses dokumen
// sungguhan.
func runChunkDemo() error {
	const simpleDoc = "halo nama saya sardi irfansyah, saya lahir di jakarta. Saya irfan. tinggal di Jakarta."

	const (
		chunkSize    = 67
		chunkOverlap = 10
	)

	fmt.Printf("Panjang total teks: %d karakter\n", len(simpleDoc))
	fmt.Printf("Konfigurasi: chunk_size=%d, chunk_overlap=%d\n\n", chunkSize, chunkOverlap)

	splitter, err := recursive.NewSplitter(context.Background(), &recursive.Config{
		ChunkSize:   chunkSize,
		OverlapSize: chunkOverlap,
	})
	if err != nil {
		return fmt.Errorf("membuat splitter: %w", err)
	}

	chunks, err := splitter.Transform(context.Background(), []*schema.Document{
		{ID: "demo", Content: simpleDoc},
	})
	if err != nil {
		return fmt.Errorf("memotong teks: %w", err)
	}

	fmt.Printf("Jumlah chunk: %d\n", len(chunks))
	for i, chunk := range chunks {
		fmt.Printf("\nChunk %d (%d karakter):\n%s\n", i+1, len(chunk.Content), chunk.Content)
		fmt.Println(strings.Repeat("-", 50))
	}

	return nil
}

// newQdrantClient membuat koneksi gRPC ke Qdrant.
//
// Perhatikan port-nya: Go client Qdrant berkomunikasi lewat gRPC di port 6334,
// bukan REST di port 6333. Port 6333 hanya dipakai untuk pengecekan manual
// memakai curl.
func newQdrantClient(cfg *config.Config) (*qdrantclient.Client, error) {
	client, err := qdrantclient.NewClient(&qdrantclient.Config{
		Host: cfg.QdrantHost,
		Port: cfg.QdrantPort,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat client Qdrant (%s:%d): %w", cfg.QdrantHost, cfg.QdrantPort, err)
	}
	return client, nil
}

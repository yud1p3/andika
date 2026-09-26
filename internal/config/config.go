// Package config membaca konfigurasi aplikasi dari file .env dan environment
// variable. Semua nilai punya default yang masuk akal sehingga aplikasi bisa
// langsung jalan hanya dengan mengisi GEMINI_API_KEY dan menyediakan dokumen
// sumber di disk.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/joho/godotenv"
)

// Config menampung seluruh konfigurasi aplikasi RAG.
type Config struct {
	// --- Gemini ---

	// GeminiAPIKey wajib diisi. Ambil dari https://aistudio.google.com.
	GeminiAPIKey string
	// ChatModel adalah LLM yang dipakai untuk menyusun jawaban akhir.
	//
	// Catatan: nama model Gemini sering berubah dan model lama bisa ditutup
	// untuk pengguna baru. Notebook pelatihan memakai "gemini-2.5-flash-lite",
	// yang saat proyek ini dibuat sudah ditolak API dengan pesan
	// "no longer available to new users". Default di bawah adalah model
	// penggantinya. Kalau muncul error serupa, cek nama model yang masih
	// tersedia lewat:
	//
	//	curl "https://generativelanguage.googleapis.com/v1beta/models?key=$GEMINI_API_KEY"
	ChatModel string
	// EmbedModel adalah model yang mengubah teks menjadi vector.
	EmbedModel string
	// EmbedDim adalah dimensi vector keluaran embedding. Nilai ini harus sama
	// dengan VectorDim yang dipakai saat membuat collection di Qdrant, karena
	// vector dengan dimensi berbeda tidak bisa dibandingkan.
	EmbedDim int32

	// --- Qdrant ---

	QdrantHost string
	// QdrantPort memakai port gRPC (6334) karena Go client Qdrant berkomunikasi
	// lewat gRPC. Port 6333 adalah REST API dan hanya dipakai untuk cek manual.
	QdrantPort       int
	QdrantCollection string

	// --- Sumber dokumen ---

	// DocPath adalah lokasi berkas dokumen sumber di disk. Ekstensi berkasnya
	// menentukan cara membacanya: ".pdf" lewat parser PDF, selebihnya dibaca
	// sebagai teks apa adanya. Jadi hasil konversi dokumen ke Markdown — misalnya
	// keluaran "xberg extract" — bisa langsung dipakai dengan mengganti nilai ini.
	DocPath string
	// DocURL bersifat opsional: kalau diisi dan DocPath belum ada di disk,
	// dokumennya diunduh dulu dari alamat ini. Dikosongkan berarti dokumen
	// dianggap sudah tersedia lokal dan tidak ada yang perlu diunduh.
	DocURL string

	// --- Tampilan web ---

	// AppTitle adalah judul yang dipajang di kepala halaman web. Dipisahkan dari
	// template supaya mengganti dokumen sumber tidak memerlukan perubahan HTML.
	AppTitle string
	// AppSubtitle adalah keterangan di bawah judul, biasanya menyebut dokumen
	// yang jadi sumber pengetahuan.
	AppSubtitle string

	// --- Chunking ---

	// ChunkStrategy menentukan cara dokumen dipotong.
	//
	//	"pasal"     (default) — potong pada batas Pasal, sehingga setiap
	//	            potongan membawa rujukan pasalnya sendiri
	//	"recursive" — potong berdasarkan ukuran, sama seperti notebook
	//
	// Yang kedua disediakan untuk membandingkan hasil dan sebagai jalan
	// mundur; lihat bagian chunking di README untuk alasannya.
	ChunkStrategy string
	// ChunkSize adalah panjang maksimal satu potongan teks (dalam karakter).
	//
	// Pada strategi "pasal" nilai ini dipakai sebagai ambang: pasal yang lebih
	// pendek dari ini disimpan utuh, yang lebih panjang dipecah pada batas
	// ayat. Menaikkannya (misalnya ke 800) mengurangi jumlah potongan.
	ChunkSize int
	// ChunkOverlap adalah jumlah karakter yang diulang di awal potongan
	// berikutnya, supaya informasi di batas potongan tidak hilang.
	//
	// Hanya dipakai oleh strategi "recursive" dan oleh bagian dokumen yang
	// tidak berpasal (pembukaan, lampiran). Pemecahan pada batas ayat tidak
	// memerlukannya karena setiap potongan sudah utuh secara makna.
	ChunkOverlap int

	// --- Retrieval ---

	// TopK adalah jumlah potongan teratas yang diambil retriever. Padanan
	// parameter "k" pada notebook.
	TopK int

	// --- Memori percakapan ---

	// HistoryMessages adalah jumlah pesan riwayat (tanya + jawab) yang
	// disertakan pada giliran berikutnya. 0 berarti memori dimatikan:
	// setiap pertanyaan dijawab lepas tanpa melihat percakapan sebelumnya.
	//
	// Nilai ini sekaligus menjadi batas atas jumlah panggilan model untuk
	// penulisan ulang pertanyaan; makin panjang riwayat, makin besar prompt
	// yang dikirim ke Gemini.
	HistoryMessages int
}

// Load membaca file .env (kalau ada) lalu environment variable, dan mengisi
// nilai default untuk yang tidak diset. File .env yang tidak ditemukan bukan
// error, supaya aplikasi tetap bisa dikonfigurasi murni lewat environment
// variable (misalnya di server atau container).
func Load() (*Config, error) {
	// Nilai dari environment variable yang sudah ada tidak ditimpa oleh .env.
	// root adalah folder tempat .env ditemukan (akar proyek), dipakai untuk
	// menafsirkan path relatif di bawah.
	root := loadDotEnv()

	var errs []error

	cfg := &Config{
		GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
		ChatModel:        envStr("GEMINI_CHAT_MODEL", "gemini-3.5-flash-lite"),
		EmbedModel:       envStr("GEMINI_EMBED_MODEL", "gemini-embedding-2"),
		QdrantHost:       envStr("QDRANT_HOST", "localhost"),
		QdrantCollection: envStr("QDRANT_COLLECTION", "pearson_ai"),
		DocPath:          resolvePath(root, envStr("DOC_PATH", "data/ai_pv.pdf")),
		DocURL:           os.Getenv("DOC_URL"),
		AppTitle:         envStr("APP_TITLE", "Tanya Dokumen"),
		AppSubtitle:      envStr("APP_SUBTITLE", "RAG dengan Eino, Gemini, dan Qdrant"),

		EmbedDim:      envInt32("GEMINI_EMBED_DIM", 768, &errs),
		QdrantPort:    envInt("QDRANT_PORT", 6334, &errs),
		ChunkStrategy: envStr("CHUNK_STRATEGY", "pasal"),
		ChunkSize:     envInt("CHUNK_SIZE", 500, &errs),
		ChunkOverlap:  envInt("CHUNK_OVERLAP", 100, &errs),
		TopK:          envInt("TOP_K", 10, &errs),
		// 6 pesan = 3 giliran tanya-jawab terakhir. Cukup untuk merujuk
		// "poin a di atas" tanpa membengkakkan prompt.
		HistoryMessages: envInt("HISTORY_MESSAGES", 6, &errs),
	}

	// Jumlah pesan negatif tidak punya arti. Diperlakukan sebagai 0 (memori
	// mati) alih-alih menggagalkan seluruh konfigurasi karena salah ketik.
	if cfg.HistoryMessages < 0 {
		cfg.HistoryMessages = 0
	}

	// Strategi pemotongan yang tidak dikenal dilaporkan sebagai error, bukan
	// diam-diam diganti default. Salah ketik yang senyap di sini berbahaya:
	// hasil ingest tetap terlihat benar, padahal dokumennya dipotong dengan
	// cara yang berbeda dari yang dikira.
	switch cfg.ChunkStrategy {
	case "pasal", "recursive":
	default:
		errs = append(errs, fmt.Errorf(
			"CHUNK_STRATEGY=%q tidak dikenal; pilih \"pasal\" atau \"recursive\"",
			cfg.ChunkStrategy))
	}

	// Ukuran chunk nol atau negatif juga dilaporkan, bukan dibetulkan diam-diam.
	// Pada strategi "pasal", nilai seperti itu membuat setiap pasal dianggap
	// perlu dipecah sehingga hasilnya membingungkan tanpa error yang jelas.
	if cfg.ChunkSize <= 0 {
		errs = append(errs, fmt.Errorf(
			"CHUNK_SIZE=%d tidak masuk akal; harus lebih besar dari nol", cfg.ChunkSize))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return cfg, nil
}

// loadDotEnv mencari file .env mulai dari direktori kerja saat ini, lalu naik
// ke direktori induk sampai ketemu atau sampai akar filesystem. Nilai baliknya
// adalah folder tempat .env ditemukan, atau "" kalau tidak ada.
//
// Pencarian ke atas ini perlu karena `go test ./internal/...` menjalankan test
// dengan direktori kerja di dalam folder paket, bukan di akar proyek. Tanpa
// pencarian ini, test akan diam-diam memakai nilai default (collection
// "pearson_ai", PDF "data/ai_pv.pdf") alih-alih konfigurasi asli di .env —
// kegagalan yang menyesatkan karena tampak berhasil.
//
// Kalau .env tidak ada di mana pun, fungsi ini tidak melakukan apa-apa;
// aplikasi tetap bisa dikonfigurasi murni lewat environment variable.
func loadDotEnv() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		candidate := filepath.Join(dir, ".env")
		if _, err := os.Stat(candidate); err == nil {
			// godotenv.Load tidak menimpa environment variable yang sudah ada.
			_ = godotenv.Load(candidate)
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Sudah di akar filesystem, tidak ada lagi yang bisa dicari.
			return ""
		}
		dir = parent
	}
}

// resolvePath menafsirkan path relatif terhadap root (akar proyek). Path yang
// sudah absolut, atau root yang kosong, dikembalikan apa adanya.
//
// Ini membuat perintah yang sama bekerja baik dijalankan dari akar proyek
// maupun dari dalam paket saat test: "data/dokumen.md" selalu menunjuk berkas
// yang sama, bukan relatif terhadap direktori kerja yang kebetulan aktif.
func resolvePath(root, path string) string {
	if root == "" || path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// envStr mengembalikan nilai environment variable, atau def kalau kosong.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envInt membaca environment variable sebagai int. Nilai yang tidak valid
// dicatat sebagai error dan diganti def, supaya satu salah ketik tidak
// menggagalkan seluruh konfigurasi.
func envInt(key string, def int, errs *[]error) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}

	v, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s=%q bukan angka yang valid: %w", key, raw, err))
		return def
	}

	return v
}

// envInt32 sama seperti envInt tapi menghasilkan int32, dipakai untuk
// OutputDimensionality yang bertipe *int32.
func envInt32(key string, def int32, errs *[]error) int32 {
	return int32(envInt(key, int(def), errs))
}

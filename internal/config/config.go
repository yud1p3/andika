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
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config menampung seluruh konfigurasi aplikasi RAG.
type Config struct {
	// --- Gemini ---

	// GeminiAPIKey wajib diisi. Ambil dari https://aistudio.google.com.
	//
	// Kunci ini selalu dibutuhkan, bahkan ketika ChatProvider="openai":
	// embedding tetap lewat API resmi Google karena antarmuka web Gemini
	// tidak menyediakan endpoint embedding.
	GeminiAPIKey string
	// ChatProvider menentukan backend untuk chat model.
	//
	//	"gemini" (default) — API resmi Google, memakai GeminiAPIKey
	//	"openai"           — endpoint OpenAI-compatible, misalnya proxy
	//	                     gemini-web2api (lihat ChatBaseURL/ChatAPIKey)
	//
	// Hanya chat model yang beralih; embedder tidak terpengaruh.
	ChatProvider string
	// ChatBaseURL adalah alamat dasar endpoint OpenAI-compatible, dipakai
	// hanya ketika ChatProvider="openai". Default menunjuk proxy
	// gemini-web2api yang berjalan di mesin yang sama.
	ChatBaseURL string
	// ChatAPIKey adalah kunci untuk endpoint OpenAI-compatible, dipakai hanya
	// ketika ChatProvider="openai". Ini kunci lokal proxy (mis. "sk-gemini"),
	// bukan kunci Google, jadi aman disimpan di .env.
	ChatAPIKey string
	// ChatModel adalah LLM yang dipakai untuk menyusun jawaban akhir.
	//
	// Ketika ChatProvider="openai", nilainya harus nama model yang dikenali
	// proxy (mis. "gemini-3.6-flash"), bukan nama model API Google.
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

	// --- Kode klasifikasi ---

	// DatabaseURL adalah string koneksi PostgreSQL tempat kode klasifikasi
	// beserta vektornya disimpan. Dipakai hanya oleh perintah "rag impor-kode";
	// jalur baca (rag serve) tidak menyentuh PostgreSQL sama sekali karena
	// datanya sudah disalin ke Qdrant.
	//
	// Dibiarkan kosong di Load supaya "ask" dan "serve" tetap bisa jalan tanpa
	// database. Perintah impor yang akan melaporkan sendiri kalau nilainya
	// kosong.
	DatabaseURL string
	// KodeCollection adalah collection Qdrant khusus kode klasifikasi.
	//
	// Sengaja dipisah dari QdrantCollection (isi dokumen peraturan): isinya
	// berbeda jenis — satu kode satu titik, bukan potongan pasal — sehingga
	// menggabungkannya hanya akan mencemari pencarian perbup.
	KodeCollection string
	// KodeTabel dan KodeKolom* menentukan dari mana data dibaca di PostgreSQL.
	//
	// Dibuat konfigurabel karena nama tabel/kolom bergantung pada skema yang
	// sudah ada di luar proyek ini. Nilainya disisipkan ke SQL, jadi tiap nama
	// divalidasi sebagai identifier sederhana (lihat cekIdentifier) — bukan
	// kutipan bebas yang bisa menyelundupkan perintah lain.
	//
	// KodeKolomID adalah kunci unik baris. Ia dipakai sebagai ID titik Qdrant
	// karena kolom kode TIDAK unik: satu kode bisa dipakai beberapa item
	// (mis. dua sub-bidang yang berkode sama). Memakai kode sebagai ID akan
	// membuat baris-baris itu saling menimpa.
	KodeTabel       string
	KodeKolomID     string
	KodeKolomKode   string
	KodeKolomUraian string
	KodeKolomVektor string
	// KodeKolomPath adalah kolom hierarki lengkap ("A > B > C"). Inilah teks
	// yang di-embed saat vektor kode dibuat, jadi ia juga yang paling berguna
	// disertakan ke model saat memilih kode.
	KodeKolomPath string
	// KodeTopK adalah jumlah kandidat kode yang diambil dari Qdrant untuk
	// diserahkan ke model saat pengguna mengetik KK#perihal#.
	KodeTopK int

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

	// SaranCount adalah jumlah tombol pertanyaan lanjutan yang disusun setelah
	// sebuah jawaban di client web. 0 mematikan fitur: model tidak dipanggil
	// kedua kali, dan tidak ada tombol yang ditampilkan.
	//
	// Dipakai hanya oleh "rag serve"; "rag ask" tidak menyentuhnya.
	SaranCount int

	// --- Mode agen (eksperimental) ---

	// AgentMode mengaktifkan jalur agen ReAct sebagai pengganti chain linear.
	//
	// Default false: chain linear tetap jalur cepat bawaan. Ketika diaktifkan,
	// agen memakai tool pencarian untuk memutuskan sendiri langkahnya, dan
	// chain linear dipasang sebagai cadangan bila agen gagal sebelum
	// mengeluarkan potongan jawaban pertama.
	AgentMode bool
	// AgentMaxStep membatasi jumlah langkah graph agen. Satu panggilan model
	// atau satu pemanggilan tool dihitung satu langkah. Bila habis, agen
	// gagal dan permintaan jatuh ke chain linear.
	//
	// Default 8 kira-kira cukup untuk baca_pasal -> ikuti_rujukan -> jawab,
	// sambil menahan loop yang tidak kunjung selesai.
	AgentMaxStep int
	// AgentTimeoutSeconds adalah jaring pengaman waktu untuk satu permintaan
	// di jalur agen. Batas yang sebenarnya adalah AgentMaxStep; nilai ini
	// hanya mencegah satu permintaan menggantung tanpa batas.
	AgentTimeoutSeconds int
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
		ChatProvider:     envStr("CHAT_PROVIDER", "gemini"),
		ChatBaseURL:      envStr("CHAT_BASE_URL", "http://localhost:8081/v1"),
		ChatAPIKey:       envStr("CHAT_API_KEY", "sk-gemini"),
		ChatModel:        envStr("GEMINI_CHAT_MODEL", "gemini-3.5-flash-lite"),
		EmbedModel:       envStr("GEMINI_EMBED_MODEL", "gemini-embedding-2"),
		QdrantHost:       envStr("QDRANT_HOST", "localhost"),
		QdrantCollection: envStr("QDRANT_COLLECTION", "pearson_ai"),
		DocPath:          resolvePath(root, envStr("DOC_PATH", "data/ai_pv.pdf")),
		DocURL:           os.Getenv("DOC_URL"),
		AppTitle:         envStr("APP_TITLE", "Tanya Dokumen"),
		AppSubtitle:      envStr("APP_SUBTITLE", "RAG dengan Eino, Gemini, dan Qdrant"),

		EmbedDim:        envInt32("GEMINI_EMBED_DIM", 768, &errs),
		QdrantPort:      envInt("QDRANT_PORT", 6334, &errs),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		KodeCollection:  envStr("QDRANT_COLLECTION_KODE", "kode_klasifikasi"),
		KodeTabel:       envStr("KODE_TABEL", "kode_klasifikasi"),
		KodeKolomID:     envStr("KODE_KOLOM_ID", "id"),
		KodeKolomKode:   envStr("KODE_KOLOM_KODE", "kode"),
		KodeKolomUraian: envStr("KODE_KOLOM_URAIAN", "uraian"),
		KodeKolomVektor: envStr("KODE_KOLOM_VEKTOR", "embedding"),
		KodeKolomPath:   envStr("KODE_KOLOM_PATH", "path"),
		KodeTopK:        envInt("KODE_TOP_K", 10, &errs),
		ChunkStrategy:   envStr("CHUNK_STRATEGY", "pasal"),
		ChunkSize:       envInt("CHUNK_SIZE", 500, &errs),
		ChunkOverlap:    envInt("CHUNK_OVERLAP", 100, &errs),
		TopK:            envInt("TOP_K", 10, &errs),
		// 6 pesan = 3 giliran tanya-jawab terakhir. Cukup untuk merujuk
		// "poin a di atas" tanpa membengkakkan prompt.
		HistoryMessages: envInt("HISTORY_MESSAGES", 6, &errs),
		// 3 tombol cukup untuk membuka arah lanjutan tanpa memenuhi layar,
		// dan tiap saran berarti satu panggilan model tambahan per jawaban.
		SaranCount: envInt("SARAN_COUNT", 3, &errs),

		AgentMode:           envBool("AGENT_MODE", false, &errs),
		AgentMaxStep:        envInt("AGENT_MAX_STEP", 8, &errs),
		AgentTimeoutSeconds: envInt("AGENT_TIMEOUT_SECONDS", 90, &errs),
	}

	// Jumlah pesan negatif tidak punya arti. Diperlakukan sebagai 0 (memori
	// mati) alih-alih menggagalkan seluruh konfigurasi karena salah ketik.
	if cfg.HistoryMessages < 0 {
		cfg.HistoryMessages = 0
	}

	// Sama untuk jumlah saran: negatif diperlakukan sebagai 0 (fitur mati).
	if cfg.SaranCount < 0 {
		cfg.SaranCount = 0
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

	// Penyedia chat yang tidak dikenal dilaporkan sebagai error, bukan
	// diam-diam diganti default. Salah ketik di sini berbahaya karena
	// aplikasi tetap tampak berjalan — hanya saja menembak backend yang
	// berbeda dari yang dikira, dan hasilnya baru terasa saat kualitas
	// jawaban menurun.
	cfg.ChatProvider = strings.ToLower(strings.TrimSpace(cfg.ChatProvider))
	switch cfg.ChatProvider {
	case "gemini", "openai":
	default:
		errs = append(errs, fmt.Errorf(
			"CHAT_PROVIDER=%q tidak dikenal; pilih \"gemini\" atau \"openai\"",
			cfg.ChatProvider))
	}

	// Endpoint OpenAI-compatible tanpa alamat tidak bisa dihubungi. Ditolak
	// lebih awal supaya kesalahannya jelas, bukan berupa kegagalan koneksi
	// yang membingungkan saat permintaan pertama.
	if cfg.ChatProvider == "openai" && strings.TrimSpace(cfg.ChatBaseURL) == "" {
		errs = append(errs, errors.New(
			"CHAT_BASE_URL kosong padahal CHAT_PROVIDER=openai; "+
				"isi alamat proxy, misalnya http://localhost:8081/v1"))
	}

	// Ukuran chunk nol atau negatif juga dilaporkan, bukan dibetulkan diam-diam.
	// Pada strategi "pasal", nilai seperti itu membuat setiap pasal dianggap
	// perlu dipecah sehingga hasilnya membingungkan tanpa error yang jelas.
	if cfg.ChunkSize <= 0 {
		errs = append(errs, fmt.Errorf(
			"CHUNK_SIZE=%d tidak masuk akal; harus lebih besar dari nol", cfg.ChunkSize))
	}

	// Batas langkah agen nol atau negatif membuat agen gagal sebelum sempat
	// memanggil tool mana pun. Lebih baik dilaporkan daripada membingungkan.
	if cfg.AgentMaxStep < 1 {
		errs = append(errs, fmt.Errorf(
			"AGENT_MAX_STEP=%d tidak masuk akal; harus minimal 1", cfg.AgentMaxStep))
	}

	if cfg.AgentTimeoutSeconds < 1 {
		errs = append(errs, fmt.Errorf(
			"AGENT_TIMEOUT_SECONDS=%d tidak masuk akal; harus minimal 1", cfg.AgentTimeoutSeconds))
	}

	// Nama tabel dan kolom kode klasifikasi disisipkan langsung ke SQL, jadi
	// tiap nama harus identifier sederhana. Nilai aneh dilaporkan, bukan
	// dibiarkan lewat: selain berisiko, salah ketik di sini baru terasa sebagai
	// error SQL yang membingungkan saat impor.
	for _, id := range []struct{ nama, nilai string }{
		{"KODE_TABEL", cfg.KodeTabel},
		{"KODE_KOLOM_ID", cfg.KodeKolomID},
		{"KODE_KOLOM_KODE", cfg.KodeKolomKode},
		{"KODE_KOLOM_URAIAN", cfg.KodeKolomUraian},
		{"KODE_KOLOM_VEKTOR", cfg.KodeKolomVektor},
		{"KODE_KOLOM_PATH", cfg.KodeKolomPath},
	} {
		if !cekIdentifier(id.nilai) {
			errs = append(errs, fmt.Errorf(
				"%s=%q bukan nama tabel/kolom yang sah; pakai huruf, angka, dan garis bawah, "+
					"tanpa spasi atau tanda kutip", id.nama, id.nilai))
		}
	}

	// Jumlah kandidat kode nol atau negatif membuat model tidak pernah menerima
	// bahan apa pun. Lebih baik dilaporkan daripada menghasilkan rekomendasi
	// kosong tanpa penjelasan.
	if cfg.KodeTopK < 1 {
		errs = append(errs, fmt.Errorf(
			"KODE_TOP_K=%d tidak masuk akal; harus minimal 1", cfg.KodeTopK))
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

// reIdentifier cocok dengan nama tabel/kolom SQL yang sederhana: diawali huruf
// atau garis bawah, dilanjutkan huruf, angka, atau garis bawah.
var reIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// cekIdentifier memastikan sebuah nama aman disisipkan ke SQL tanpa kutip.
//
// Nama tabel dan kolom kode klasifikasi dirakit ke query lewat Sprintf, jadi
// nilai dari .env tidak boleh memuat spasi, tanda kutip, titik, atau titik dua
// yang bisa mengubah arti query. Nama bertitik (skema.tabel) sengaja tidak
// diterima supaya aturannya satu dan mudah diperiksa.
func cekIdentifier(nama string) bool {
	return reIdentifier.MatchString(nama)
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

// envBool membaca environment variable sebagai boolean. Nilai yang tidak valid
// dicatat sebagai error dan diganti def, mengikuti pola envInt.
//
// Selain "true"/"false" baku, diterima juga "ya"/"tidak" dan "on"/"off" supaya
// berkas .env berbahasa Indonesia tidak terasa asing. Pencocokan tidak peka
// huruf besar-kecil dan spasi di ujung diabaikan.
func envBool(key string, def bool, errs *[]error) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return def
	}

	switch raw {
	case "1", "true", "ya", "yes", "on":
		return true
	case "0", "false", "tidak", "no", "off":
		return false
	default:
		*errs = append(*errs, fmt.Errorf(
			"%s=%q bukan boolean yang valid (pakai true/false)", key, raw))
		return def
	}
}

package config

import "testing"

// TestResolvePath mengunci perilaku penafsiran path relatif terhadap akar
// proyek.
//
// Ini menjaga perbaikan bug: dulu DocPath relatif ditafsirkan terhadap
// direktori kerja aktif, sehingga `go test ./internal/ingest/` mencari berkas
// di internal/ingest/data/... dan tes diam-diam ter-skip (tampak berhasil).
func TestResolvePath(t *testing.T) {
	cases := []struct {
		nama string
		root string
		path string
		want string
	}{
		{"relatif digabung ke akar", "/proyek", "data/dok.pdf", "/proyek/data/dok.pdf"},
		{"absolut dibiarkan", "/proyek", "/lain/dok.pdf", "/lain/dok.pdf"},
		{"root kosong dibiarkan relatif", "", "data/dok.pdf", "data/dok.pdf"},
		{"path kosong dibiarkan kosong", "/proyek", "", ""},
	}

	for _, c := range cases {
		t.Run(c.nama, func(t *testing.T) {
			if got := resolvePath(c.root, c.path); got != c.want {
				t.Errorf("resolvePath(%q, %q) = %q, mau %q", c.root, c.path, got, c.want)
			}
		})
	}
}

// TestChunkStrategyDefault mengunci default strategi pemotongan.
//
// Nilainya penting karena menentukan bentuk seluruh isi collection: strategi
// "pasal" membuat setiap potongan membawa rujukan pasalnya sendiri, sedangkan
// "recursive" tidak.
func TestChunkStrategyDefault(t *testing.T) {
	t.Setenv("CHUNK_STRATEGY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.ChunkStrategy != "pasal" {
		t.Errorf("ChunkStrategy = %q, mau %q", cfg.ChunkStrategy, "pasal")
	}
}

// TestChunkStrategyTidakDikenalDilaporkan memastikan salah ketik dilaporkan,
// bukan diam-diam diganti default.
//
// Salah ketik yang senyap di sini berbahaya: hasil ingest tetap terlihat benar
// padahal dokumennya dipotong dengan cara yang berbeda dari yang dikira.
func TestChunkStrategyTidakDikenalDilaporkan(t *testing.T) {
	t.Setenv("CHUNK_STRATEGY", "per-pasal")

	if _, err := Load(); err == nil {
		t.Fatal("Load() berhasil, mau error karena CHUNK_STRATEGY tidak dikenal")
	}
}

// TestChunkStrategyRecursiveDiterima memastikan jalan mundur ke cara notebook
// tetap tersedia.
func TestChunkStrategyRecursiveDiterima(t *testing.T) {
	t.Setenv("CHUNK_STRATEGY", "recursive")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.ChunkStrategy != "recursive" {
		t.Errorf("ChunkStrategy = %q, mau %q", cfg.ChunkStrategy, "recursive")
	}
}

// TestChunkSizeTidakMasukAkalDilaporkan menjaga ambang pemecahan pasal tetap
// bermakna.
//
// Pada strategi "pasal", ChunkSize nol membuat setiap pasal dianggap perlu
// dipecah — hasil yang membingungkan tanpa error yang jelas, jadi lebih baik
// ditolak di depan.
func TestChunkSizeTidakMasukAkalDilaporkan(t *testing.T) {
	for _, nilai := range []string{"0", "-100"} {
		t.Run("CHUNK_SIZE="+nilai, func(t *testing.T) {
			t.Setenv("CHUNK_SIZE", nilai)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() berhasil dengan CHUNK_SIZE=%s, mau error", nilai)
			}
		})
	}
}

// TestSaranCountDefault mengunci jumlah tombol saran bawaan.
//
// Nilainya menentukan berapa panggilan model tambahan yang terjadi pada tiap
// jawaban di client web, jadi mengubahnya tanpa sadar akan terasa sebagai
// beban kuota yang berbeda.
func TestSaranCountDefault(t *testing.T) {
	t.Setenv("SARAN_COUNT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.SaranCount != 3 {
		t.Errorf("SaranCount = %d, mau 3", cfg.SaranCount)
	}
}

// TestSaranCountNegatifJadiNol memastikan salah ketik tidak diam-diam
// menghidupkan fitur.
//
// Sama seperti HISTORY_MESSAGES: satu nilai punya satu arti. Negatif
// diperlakukan sebagai 0 (fitur mati), bukan dibiarkan lewat dan membuat
// pemanggil menebak-nebak.
func TestSaranCountNegatifJadiNol(t *testing.T) {
	t.Setenv("SARAN_COUNT", "-2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.SaranCount != 0 {
		t.Errorf("SaranCount = %d, mau 0", cfg.SaranCount)
	}
}

// TestSaranCountBukanAngkaDilaporkan memastikan salah ketik dilaporkan, bukan
// diganti default diam-diam.
func TestSaranCountBukanAngkaDilaporkan(t *testing.T) {
	t.Setenv("SARAN_COUNT", "tiga")

	if _, err := Load(); err == nil {
		t.Fatal("Load() berhasil, mau error karena SARAN_COUNT bukan angka")
	}
}

// TestAgentModeDefaultFalse mengunci bahwa jalur cepat (chain linear) tetap
// menjadi bawaan. Agen harus dinyalakan secara sadar.
func TestAgentModeDefaultFalse(t *testing.T) {
	t.Setenv("AGENT_MODE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.AgentMode {
		t.Error("AgentMode = true, mau false (chain linear tetap bawaan)")
	}
}

// TestAgentModeBoolDiterima memastikan bentuk penulisan yang lazim — termasuk
// "ya" berbahasa Indonesia — dikenali.
func TestAgentModeBoolDiterima(t *testing.T) {
	for _, nilai := range []string{"true", "1", "ya", "on", "YES"} {
		t.Run("AGENT_MODE="+nilai, func(t *testing.T) {
			t.Setenv("AGENT_MODE", nilai)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() gagal: %v", err)
			}
			if !cfg.AgentMode {
				t.Errorf("AgentMode = false dengan AGENT_MODE=%q, mau true", nilai)
			}
		})
	}
}

// TestAgentModeBoolTidakValidDilaporkan memastikan nilai yang bukan boolean
// dilaporkan, bukan diam-diam dianggap mati.
func TestAgentModeBoolTidakValidDilaporkan(t *testing.T) {
	t.Setenv("AGENT_MODE", "iya")

	if _, err := Load(); err == nil {
		t.Fatal("Load() berhasil, mau error karena AGENT_MODE bukan boolean")
	}
}

// TestAgentMaxStepTidakMasukAkalDilaporkan menjaga batas loop agen tetap
// bermakna. Nilai nol membuat agen gagal sebelum memanggil tool apa pun.
func TestAgentMaxStepTidakMasukAkalDilaporkan(t *testing.T) {
	for _, nilai := range []string{"0", "-1"} {
		t.Run("AGENT_MAX_STEP="+nilai, func(t *testing.T) {
			t.Setenv("AGENT_MAX_STEP", nilai)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() berhasil dengan AGENT_MAX_STEP=%s, mau error", nilai)
			}
		})
	}
}

// TestAgentMaxStepDefault mengunci batas langkah bawaan. Nilainya menentukan
// berapa panggilan model paling banyak yang boleh terjadi per pertanyaan di
// jalur agen, jadi mengubahnya tanpa sadar mengubah biaya kuota.
func TestAgentMaxStepDefault(t *testing.T) {
	t.Setenv("AGENT_MAX_STEP", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.AgentMaxStep != 8 {
		t.Errorf("AgentMaxStep = %d, mau 8", cfg.AgentMaxStep)
	}
}

// TestAgentTimeoutDefault mengunci jaring pengaman waktu bawaan jalur agen.
func TestAgentTimeoutDefault(t *testing.T) {
	t.Setenv("AGENT_TIMEOUT_SECONDS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.AgentTimeoutSeconds != 90 {
		t.Errorf("AgentTimeoutSeconds = %d, mau 90", cfg.AgentTimeoutSeconds)
	}
}

// TestChatProviderDefaultGemini mengunci bahwa API resmi Google tetap menjadi
// jalur bawaan. Proxy eksperimental harus dipilih secara sadar, supaya
// pemasangan yang sudah ada tidak diam-diam berubah perilaku setelah upgrade.
func TestChatProviderDefaultGemini(t *testing.T) {
	t.Setenv("CHAT_PROVIDER", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.ChatProvider != "gemini" {
		t.Errorf("ChatProvider = %q, mau %q", cfg.ChatProvider, "gemini")
	}
}

// TestChatProviderOpenAIDiterima memastikan nilai yang lazim diketik — beda
// huruf besar-kecil dan spasi di ujung — tetap dikenali.
func TestChatProviderOpenAIDiterima(t *testing.T) {
	for _, nilai := range []string{"openai", "OpenAI", "OPENAI", "  openai  "} {
		t.Run("CHAT_PROVIDER="+nilai, func(t *testing.T) {
			t.Setenv("CHAT_PROVIDER", nilai)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() gagal: %v", err)
			}
			if cfg.ChatProvider != "openai" {
				t.Errorf("ChatProvider = %q dengan CHAT_PROVIDER=%q, mau %q",
					cfg.ChatProvider, nilai, "openai")
			}
		})
	}
}

// TestChatProviderTidakDikenalDilaporkan memastikan salah ketik dilaporkan,
// bukan diam-diam jatuh ke default.
//
// Salah ketik yang senyap di sini berbahaya: aplikasi tetap tampak berjalan,
// hanya saja menembak backend yang berbeda dari yang dikira.
func TestChatProviderTidakDikenalDilaporkan(t *testing.T) {
	t.Setenv("CHAT_PROVIDER", "antropik")

	if _, err := Load(); err == nil {
		t.Fatal("Load() berhasil, mau error karena CHAT_PROVIDER tidak dikenal")
	}
}

// TestChatBaseURLDefault mengunci alamat bawaan proxy gemini-web2api.
func TestChatBaseURLDefault(t *testing.T) {
	t.Setenv("CHAT_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.ChatBaseURL != "http://localhost:8081/v1" {
		t.Errorf("ChatBaseURL = %q, mau %q", cfg.ChatBaseURL, "http://localhost:8081/v1")
	}
}

// TestChatBaseURLOpenAITidakBolehKosong menjaga kesalahan konfigurasi
// tertangkap saat Load, bukan sebagai kegagalan koneksi yang membingungkan
// saat permintaan pertama.
//
// Nilainya berupa spasi, bukan string kosong: envStr memperlakukan string
// kosong sebagai "tidak diset" dan menggantinya dengan default, sehingga hanya
// nilai berisi karakter yang benar-benar bisa mengosongkan alamat ini.
func TestChatBaseURLOpenAITidakBolehKosong(t *testing.T) {
	t.Setenv("CHAT_PROVIDER", "openai")
	t.Setenv("CHAT_BASE_URL", "   ")

	if _, err := Load(); err == nil {
		t.Fatal("Load() berhasil dengan CHAT_BASE_URL kosong, mau error")
	}
}

// TestChatAPIKeyDefault mengunci kunci bawaan proxy lokal. Nilainya bukan
// rahasia Google, jadi aman berada di .env.
func TestChatAPIKeyDefault(t *testing.T) {
	t.Setenv("CHAT_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}
	if cfg.ChatAPIKey != "sk-gemini" {
		t.Errorf("ChatAPIKey = %q, mau %q", cfg.ChatAPIKey, "sk-gemini")
	}
}

// TestKodeDefault mengunci default konfigurasi kode klasifikasi.
//
// Nilainya penting karena menentukan collection Qdrant mana yang dibaca dan
// kolom mana yang diambil saat impor; mengubahnya tanpa sadar berarti impor
// membaca tabel yang salah atau menulis ke collection yang salah.
func TestKodeDefault(t *testing.T) {
	for _, k := range []string{
		"DATABASE_URL", "QDRANT_COLLECTION_KODE", "KODE_TABEL", "KODE_KOLOM_ID",
		"KODE_KOLOM_KODE", "KODE_KOLOM_URAIAN", "KODE_KOLOM_VEKTOR", "KODE_KOLOM_PATH", "KODE_TOP_K",
	} {
		t.Setenv(k, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() gagal: %v", err)
	}

	if cfg.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, mau kosong", cfg.DatabaseURL)
	}
	if cfg.KodeCollection != "kode_klasifikasi" {
		t.Errorf("KodeCollection = %q, mau %q", cfg.KodeCollection, "kode_klasifikasi")
	}
	if cfg.KodeTabel != "kode_klasifikasi" {
		t.Errorf("KodeTabel = %q, mau %q", cfg.KodeTabel, "kode_klasifikasi")
	}
	if cfg.KodeKolomID != "id" {
		t.Errorf("KodeKolomID = %q, mau %q", cfg.KodeKolomID, "id")
	}
	if cfg.KodeKolomKode != "kode" {
		t.Errorf("KodeKolomKode = %q, mau %q", cfg.KodeKolomKode, "kode")
	}
	if cfg.KodeKolomUraian != "uraian" {
		t.Errorf("KodeKolomUraian = %q, mau %q", cfg.KodeKolomUraian, "uraian")
	}
	if cfg.KodeKolomVektor != "embedding" {
		t.Errorf("KodeKolomVektor = %q, mau %q", cfg.KodeKolomVektor, "embedding")
	}
	if cfg.KodeKolomPath != "path" {
		t.Errorf("KodeKolomPath = %q, mau %q", cfg.KodeKolomPath, "path")
	}
	if cfg.KodeTopK != 10 {
		t.Errorf("KodeTopK = %d, mau 10", cfg.KodeTopK)
	}
}

// TestKodeIdentifierTidakSahDilaporkan memastikan nama tabel/kolom yang aneh
// ditolak di depan.
//
// Nilainya disisipkan ke SQL, jadi nilai seperti "kode; DROP TABLE" atau nama
// bertitik harus dilaporkan sebagai konfigurasi tidak sah, bukan diteruskan.
func TestKodeIdentifierTidakSahDilaporkan(t *testing.T) {
	for _, nilai := range []string{"kode; DROP TABLE x", "skema.tabel", "nama tabel", `"kode"`} {
		t.Run("KODE_TABEL="+nilai, func(t *testing.T) {
			t.Setenv("KODE_TABEL", nilai)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() berhasil dengan KODE_TABEL=%q, mau error", nilai)
			}
		})
	}
}

// TestKodeTopKTidakMasukAkalDilaporkan menjaga jumlah kandidat tetap bermakna.
// Nol berarti model tidak pernah menerima bahan apa pun.
func TestKodeTopKTidakMasukAkalDilaporkan(t *testing.T) {
	for _, nilai := range []string{"0", "-3"} {
		t.Run("KODE_TOP_K="+nilai, func(t *testing.T) {
			t.Setenv("KODE_TOP_K", nilai)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() berhasil dengan KODE_TOP_K=%s, mau error", nilai)
			}
		})
	}
}

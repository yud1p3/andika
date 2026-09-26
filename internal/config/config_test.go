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

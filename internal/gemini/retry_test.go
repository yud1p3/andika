package gemini

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/embedding"
)

// Kedua pesan di bawah disalin dari error asli Gemini saat ingest Buku KIA,
// dengan bagian yang tidak relevan dipangkas. Yang penting adalah quotaId-nya:
// satu berakhiran "PerMinute", satu "PerDay".
const (
	errKuotaPerMenit = `Error 429, Message: You exceeded your current quota, ` +
		`* Quota exceeded for metric: generativelanguage.googleapis.com/embed_content_free_tier_requests, ` +
		`limit: 100, model: gemini-embedding-2 Please retry in 11.563501324s., Status: RESOURCE_EXHAUSTED, ` +
		`quotaId:EmbedContentRequestsPerMinutePerUserPerProjectPerModel-FreeTier quotaValue:100`

	errKuotaPerHari = `Error 429, Message: You exceeded your current quota, ` +
		`* Quota exceeded for metric: generativelanguage.googleapis.com/embed_content_free_tier_requests, ` +
		`limit: 1000, model: gemini-embedding-2 Please retry in 53.649906884s., Status: RESOURCE_EXHAUSTED, ` +
		`quotaId:EmbedContentRequestsPerDayPerUserPerProjectPerModel-FreeTier quotaValue:1000`
)

// TestIsDailyQuotaError memastikan kuota harian dan kuota per menit dibedakan.
//
// Ini menentukan perilaku yang sangat berbeda: kuota per menit cukup ditunggu,
// sedangkan kuota harian tidak akan pulih sampai keesokan hari sehingga
// mengulang hanya membuang waktu.
func TestIsDailyQuotaError(t *testing.T) {
	if !isDailyQuotaError(errors.New(errKuotaPerHari)) {
		t.Error("kuota harian tidak terdeteksi sebagai kuota harian")
	}

	if isDailyQuotaError(errors.New(errKuotaPerMenit)) {
		t.Error("kuota per menit salah dideteksi sebagai kuota harian")
	}
}

func TestIsQuotaError(t *testing.T) {
	for name, err := range map[string]error{
		"per menit": errors.New(errKuotaPerMenit),
		"per hari":  errors.New(errKuotaPerHari),
	} {
		if !isQuotaError(err) {
			t.Errorf("error kuota %s tidak terdeteksi", name)
		}
	}

	// Error yang bukan soal kuota tidak boleh dianggap kuota, supaya tidak
	// diulang sia-sia.
	if isQuotaError(errors.New("Error 400, API key not valid")) {
		t.Error("error API key salah dianggap sebagai error kuota")
	}
}

func TestSuggestedDelay(t *testing.T) {
	t.Run("memakai saran dari API", func(t *testing.T) {
		// "retry in 11.563501324s" + margin 1 detik => sekitar 12,5 detik.
		got := suggestedDelay(errors.New("Please retry in 11.563501324s."), 5*time.Second)
		if got < 12*time.Second || got > 13*time.Second {
			t.Errorf("jeda = %s, diharapkan sekitar 12,5 detik", got)
		}
	})

	t.Run("fallback kalau tidak ada saran", func(t *testing.T) {
		got := suggestedDelay(errors.New("pesan tanpa saran jeda"), 7*time.Second)
		if got != 7*time.Second {
			t.Errorf("jeda = %s, diharapkan fallback 7s", got)
		}
	})

	t.Run("saran keterlaluan dibatasi", func(t *testing.T) {
		got := suggestedDelay(errors.New("Please retry in 9999s."), 5*time.Second)
		if got != 2*time.Minute {
			t.Errorf("jeda = %s, diharapkan dibatasi 2 menit", got)
		}
	})
}

// TestRetryingEmbedderMenyerahCepatPadaKuotaHarian memastikan kuota harian
// tidak memicu pengulangan berulang kali.
//
// Sebelum perbaikan ini, kuota harian yang sudah habis membuat program
// mengulang 12 kali dengan jeda hingga satu menit — sekitar 10 menit terbuang
// untuk sesuatu yang tidak mungkin berhasil.
func TestRetryingEmbedderMenyerahCepatPadaKuotaHarian(t *testing.T) {
	inner := &embedderPenghitung{err: errors.New(errKuotaPerHari)}
	emb := withRetry(inner)

	start := time.Now()
	_, err := emb.EmbedStrings(t.Context(), []string{"apa itu MPASI"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("diharapkan error, tapi berhasil")
	}
	if inner.panggilan != 1 {
		t.Errorf("embedder dipanggil %d kali, diharapkan cukup 1 kali", inner.panggilan)
	}
	if elapsed > time.Second {
		t.Errorf("butuh %s, diharapkan menyerah seketika", elapsed)
	}
	if !strings.Contains(err.Error(), "HARIAN") {
		t.Errorf("pesan error tidak menjelaskan kuota harian: %v", err)
	}
}

// embedderPenghitung adalah Embedder palsu yang selalu gagal dan mencatat
// berapa kali dipanggil.
type embedderPenghitung struct {
	err       error
	panggilan int
}

func (e *embedderPenghitung) EmbedStrings(
	_ context.Context, _ []string, _ ...embedding.Option,
) ([][]float64, error) {
	e.panggilan++
	return nil, e.err
}

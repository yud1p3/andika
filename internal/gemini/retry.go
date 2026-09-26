package gemini

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/embedding"
)

// maxEmbedAttempts adalah jumlah maksimal percobaan per panggilan EmbedStrings
// sebelum menyerah.
const maxEmbedAttempts = 12

// retryDelayPattern mengambil saran jeda dari pesan error Gemini, yang
// bentuknya "... Please retry in 11.563501324s. ...".
var retryDelayPattern = regexp.MustCompile(`retry in ([0-9.]+)s`)

// retryingEmbedder membungkus Embedder dan mengulang otomatis ketika Gemini
// menolak karena kuota.
//
// Ini diperlukan karena kuota free tier Gemini membatasi jumlah teks yang boleh
// di-embed per menit — 100 teks untuk gemini-embedding-2. Dokumen berisi
// ratusan chunk pasti menabrak batas itu, dan tanpa pengulangan seluruh proses
// ingest gagal di tengah jalan setelah sebagian data terlanjur tersimpan.
//
// Perhatikan bahwa BatchSize pada indexer Qdrant TIDAK menolong di sini. Ia
// hanya menentukan berapa teks yang diserahkan ke embedder dalam satu panggilan,
// sedangkan kuota Gemini dihitung per teks, bukan per panggilan HTTP. Karena
// itu memperbesar BatchSize tidak akan menghindarkan dari batas ini.
type retryingEmbedder struct {
	inner embedding.Embedder
}

// withRetry membungkus sebuah Embedder dengan pengulangan otomatis saat kuota
// tercapai.
func withRetry(inner embedding.Embedder) embedding.Embedder {
	return &retryingEmbedder{inner: inner}
}

func (r *retryingEmbedder) EmbedStrings(
	ctx context.Context, texts []string, opts ...embedding.Option,
) ([][]float64, error) {
	// Jeda awal kalau API tidak memberi saran; naik dua kali lipat tiap gagal.
	delay := 5 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxEmbedAttempts; attempt++ {
		vecs, err := r.inner.EmbedStrings(ctx, texts, opts...)
		if err == nil {
			return vecs, nil
		}

		// Error selain kuota (misalnya API key salah) tidak ada gunanya
		// diulang, langsung kembalikan.
		if !isQuotaError(err) {
			return nil, err
		}

		// Kuota HARIAN tidak pulih dengan menunggu sebentar, jadi mengulang
		// belasan kali hanya membuang waktu. Langsung menyerah dengan pesan
		// yang menjelaskan duduk perkaranya.
		if isDailyQuotaError(err) {
			return nil, fmt.Errorf(
				"kuota embedding HARIAN Gemini sudah habis. Menunggu tidak akan menolong — "+
					"batas ini baru pulih keesokan harinya (kuota free tier: 1000 teks per hari). "+
					"Pakai API key berbayar, atau lanjutkan besok: %w", err)
		}

		lastErr = err
		wait := suggestedDelay(err, delay)
		log.Printf("kuota per menit tercapai, menunggu %s (percobaan %d dari maksimal %d untuk batch ini)...",
			wait.Round(time.Second), attempt, maxEmbedAttempts)

		// select dipakai supaya Ctrl-C tetap bisa membatalkan penantian.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}

		if delay < time.Minute {
			delay *= 2
		}
	}

	return nil, fmt.Errorf(
		"kuota embedding Gemini masih tercapai setelah %d percobaan: %w",
		maxEmbedAttempts, lastErr)
}

// isQuotaError mendeteksi error kuota dari Gemini API.
func isQuotaError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "RESOURCE_EXHAUSTED") ||
		strings.Contains(strings.ToLower(msg), "quota")
}

// isDailyQuotaError mendeteksi habisnya kuota HARIAN, yang tidak bisa
// diselesaikan dengan menunggu.
//
// Gemini memakai dua batas berbeda dan pesan errornya sangat mirip, sehingga
// keduanya perlu dibedakan dari quotaId yang disertakan di detail error:
//
//   - "...RequestsPerMinutePerUserPerProjectPerModel-FreeTier", misalnya 100
//     teks per menit. Menunggu lalu mengulang memang menolong.
//   - "...RequestsPerDayPerUserPerProjectPerModel-FreeTier", misalnya 1000
//     teks per hari. Menunggu tidak menolong sama sekali; batas ini baru
//     pulih keesokan harinya.
func isDailyQuotaError(err error) bool {
	return strings.Contains(err.Error(), "PerDay")
}

// suggestedDelay memakai saran jeda dari pesan error kalau ada, kalau tidak
// memakai fallback.
func suggestedDelay(err error, fallback time.Duration) time.Duration {
	m := retryDelayPattern.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return fallback
	}

	secs, parseErr := strconv.ParseFloat(m[1], 64)
	if parseErr != nil || secs <= 0 {
		return fallback
	}

	// Tambah margin satu detik supaya tidak menabrak batas lagi tepat setelah
	// jeda habis.
	d := time.Duration(secs*float64(time.Second)) + time.Second
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}

	return d
}

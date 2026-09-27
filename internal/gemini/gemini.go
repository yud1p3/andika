// Package gemini menyiapkan komponen yang dibutuhkan pipeline RAG: satu chat
// model untuk menyusun jawaban, dan dua embedder untuk mengubah teks menjadi
// vector.
//
// Chat model bisa dilayani dua backend, dipilih lewat cfg.ChatProvider:
//
//   - "gemini" (default) — API resmi Google lewat GEMINI_API_KEY.
//   - "openai"           — endpoint OpenAI-compatible, misalnya proxy
//     gemini-web2api yang membungkus antarmuka web Gemini.
//
// Embedder tidak punya pilihan: keduanya selalu lewat API resmi Google, karena
// antarmuka web Gemini tidak menyediakan endpoint embedding. Jadi
// GEMINI_API_KEY tetap wajib walau chat-nya dialihkan ke proxy.
//
// Kenapa dua embedder? Gemini membedakan cara membuat vector untuk dokumen dan
// untuk query pencarian lewat parameter TaskType. Dokumen di-embed dengan
// RETRIEVAL_DOCUMENT, sedangkan pertanyaan user di-embed dengan RETRIEVAL_QUERY.
// Notebook pelatihan memakai satu embedder untuk keduanya; memisahkannya
// seperti ini menghasilkan pencarian yang lebih akurat.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"log"

	geminiembed "github.com/cloudwego/eino-ext/components/embedding/gemini"
	geminimodel "github.com/cloudwego/eino-ext/components/model/gemini"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"google.golang.org/genai"

	"rag-eino/internal/config"
)

// TaskType yang dikenali Gemini untuk keperluan retrieval.
const (
	// taskTypeDocument dipakai saat meng-embed isi dokumen.
	taskTypeDocument = "RETRIEVAL_DOCUMENT"
	// taskTypeQuery dipakai saat meng-embed pertanyaan user.
	taskTypeQuery = "RETRIEVAL_QUERY"
)

// Nama penyedia chat yang dikenali. Nilai ini sama dengan yang divalidasi di
// config.Load, jadi salah ketik sudah tertangkap sebelum sampai ke sini.
const (
	providerGemini = "gemini"
	providerOpenAI = "openai"
)

// Client menyatukan seluruh komponen yang dipakai aplikasi.
type Client struct {
	// ChatModel menghasilkan jawaban akhir dari konteks + pertanyaan.
	ChatModel model.ToolCallingChatModel
	// DocEmbedder dipakai di jalur tulis (indexing chunk ke Qdrant).
	DocEmbedder embedding.Embedder
	// QueryEmbedder dipakai di jalur baca (mencari chunk yang relevan).
	QueryEmbedder embedding.Embedder
}

// New membuat chat model dan kedua embedder.
//
// Dimensi vector (cfg.EmbedDim) diteruskan ke embedder sebagai
// OutputDimensionality dan harus sama dengan VectorDim collection Qdrant,
// karena vector dengan dimensi berbeda tidak bisa dibandingkan.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	// Kunci Google tetap wajib walau chat dialihkan ke proxy: embedder di
	// bawah tidak punya jalur lain.
	if cfg.GeminiAPIKey == "" {
		return nil, errors.New(
			"GEMINI_API_KEY belum diisi. Ambil API key di https://aistudio.google.com, " +
				"lalu isi ke file .env (lihat .env.example)")
	}

	genaiClient, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.GeminiAPIKey})
	if err != nil {
		return nil, fmt.Errorf("membuat client Gemini: %w", err)
	}

	chatModel, err := newChatModel(ctx, cfg, genaiClient)
	if err != nil {
		return nil, err
	}

	dim := cfg.EmbedDim

	docEmbedder, err := geminiembed.NewEmbedder(ctx, &geminiembed.EmbeddingConfig{
		Client:               genaiClient,
		Model:                cfg.EmbedModel,
		TaskType:             taskTypeDocument,
		OutputDimensionality: &dim,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat embedder dokumen (%s): %w", cfg.EmbedModel, err)
	}

	queryEmbedder, err := geminiembed.NewEmbedder(ctx, &geminiembed.EmbeddingConfig{
		Client:               genaiClient,
		Model:                cfg.EmbedModel,
		TaskType:             taskTypeQuery,
		OutputDimensionality: &dim,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat embedder query (%s): %w", cfg.EmbedModel, err)
	}

	return &Client{
		ChatModel: chatModel,
		// Kedua embedder dibungkus pengulangan otomatis karena kuota free tier
		// Gemini membatasi jumlah teks yang bisa di-embed per menit. Lihat
		// komentar pada retryingEmbedder di retry.go.
		DocEmbedder:   withRetry(docEmbedder),
		QueryEmbedder: withRetry(queryEmbedder),
	}, nil
}

// newChatModel memilih backend chat model sesuai cfg.ChatProvider.
//
// Dipisahkan dari New supaya pemilihannya bisa diuji tanpa menyentuh embedder
// — dan supaya jelas bahwa embedder tidak ikut beralih.
func newChatModel(ctx context.Context, cfg *config.Config, genaiClient *genai.Client) (model.ToolCallingChatModel, error) {
	if cfg.ChatProvider == providerOpenAI {
		log.Printf("gemini: chat model lewat endpoint OpenAI-compatible %s (model %q)",
			cfg.ChatBaseURL, cfg.ChatModel)
		return newChatModelOpenAI(ctx, cfg)
	}

	log.Printf("gemini: chat model lewat API resmi Google (model %q)", cfg.ChatModel)
	return newChatModelGemini(ctx, cfg, genaiClient)
}

// newChatModelGemini membuat chat model lewat API resmi Google.
func newChatModelGemini(ctx context.Context, cfg *config.Config, genaiClient *genai.Client) (model.ToolCallingChatModel, error) {
	chatModel, err := geminimodel.NewChatModel(ctx, &geminimodel.Config{
		Client: genaiClient,
		Model:  cfg.ChatModel,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat chat model %q: %w", cfg.ChatModel, err)
	}
	return chatModel, nil
}

// newChatModelOpenAI membuat chat model lewat endpoint OpenAI-compatible.
//
// BaseURL harus memuat versi API (".../v1"), karena nilai ini diteruskan apa
// adanya ke klien OpenAI dan yang ditambahkan hanyalah path relatif
// ("/chat/completions").
//
// Tidak ada pemeriksaan koneksi di sini: kegagalan proxy baru ketahuan saat
// permintaan pertama. Itu disengaja — gagal cepat di startup akan membuat
// aplikasi tidak bisa dijalankan sama sekali hanya karena proxy belum sempat
// dinyalakan, padahal jalur ingest tidak memerlukan chat model.
func newChatModelOpenAI(ctx context.Context, cfg *config.Config) (model.ToolCallingChatModel, error) {
	chatModel, err := openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
		BaseURL: cfg.ChatBaseURL,
		APIKey:  cfg.ChatAPIKey,
		Model:   cfg.ChatModel,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"membuat chat model OpenAI-compatible %q di %s: %w",
			cfg.ChatModel, cfg.ChatBaseURL, err)
	}
	return chatModel, nil
}

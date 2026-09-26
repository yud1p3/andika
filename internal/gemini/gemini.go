// Package gemini menyiapkan komponen Gemini yang dibutuhkan pipeline RAG:
// satu chat model untuk menyusun jawaban, dan dua embedder untuk mengubah teks
// menjadi vector.
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

	geminiembed "github.com/cloudwego/eino-ext/components/embedding/gemini"
	geminimodel "github.com/cloudwego/eino-ext/components/model/gemini"
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

// Client menyatukan seluruh komponen Gemini yang dipakai aplikasi.
type Client struct {
	// ChatModel menghasilkan jawaban akhir dari konteks + pertanyaan.
	ChatModel model.ToolCallingChatModel
	// DocEmbedder dipakai di jalur tulis (indexing chunk ke Qdrant).
	DocEmbedder embedding.Embedder
	// QueryEmbedder dipakai di jalur baca (mencari chunk yang relevan).
	QueryEmbedder embedding.Embedder
}

// New membuat chat model dan kedua embedder dari satu client Gemini.
//
// Dimensi vector (cfg.EmbedDim) diteruskan ke embedder sebagai
// OutputDimensionality dan harus sama dengan VectorDim collection Qdrant,
// karena vector dengan dimensi berbeda tidak bisa dibandingkan.
func New(ctx context.Context, cfg *config.Config) (*Client, error) {
	if cfg.GeminiAPIKey == "" {
		return nil, errors.New(
			"GEMINI_API_KEY belum diisi. Ambil API key di https://aistudio.google.com, " +
				"lalu isi ke file .env (lihat .env.example)")
	}

	genaiClient, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.GeminiAPIKey})
	if err != nil {
		return nil, fmt.Errorf("membuat client Gemini: %w", err)
	}

	chatModel, err := geminimodel.NewChatModel(ctx, &geminimodel.Config{
		Client: genaiClient,
		Model:  cfg.ChatModel,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat chat model %q: %w", cfg.ChatModel, err)
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

package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"

	"rag-eino/internal/config"
)

// clientGenaiUji membuat client genai dengan kunci palsu. Konstruksinya tidak
// menembak jaringan, jadi aman dipakai di test.
func clientGenaiUji(t *testing.T) *genai.Client {
	t.Helper()

	c, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: "kunci-uji"})
	if err != nil {
		t.Fatalf("membuat client genai uji: %v", err)
	}
	return c
}

// TestNewChatModelCabangGemini mengunci bahwa provider "gemini" — dan juga
// provider kosong — menghasilkan chat model lewat API resmi Google.
//
// Provider kosong penting: Config yang disusun langsung di kode (bukan lewat
// Load) akan punya field kosong, dan itu tidak boleh berubah jadi error.
func TestNewChatModelCabangGemini(t *testing.T) {
	for _, provider := range []string{"gemini", ""} {
		t.Run("provider="+provider, func(t *testing.T) {
			cfg := &config.Config{ChatProvider: provider, ChatModel: "gemini-3.5-flash-lite"}

			cm, err := newChatModel(context.Background(), cfg, clientGenaiUji(t))
			if err != nil {
				t.Fatalf("newChatModel() gagal: %v", err)
			}
			if cm == nil {
				t.Fatal("newChatModel() mengembalikan nil tanpa error")
			}
		})
	}
}

// TestChatModelOpenAIMenembakBaseURLDanBearer menguji cabang OpenAI-compatible
// sampai ke kabelnya: server tiruan menggantikan proxy gemini-web2api, lalu
// diperiksa path, header Authorization, dan nama model yang benar-benar
// dikirim.
//
// Tanpa test ini, kesalahan seperti BaseURL yang kehilangan "/v1" atau API key
// yang tidak terpasang hanya akan ketahuan saat dijalankan melawan proxy asli.
func TestChatModelOpenAIMenembakBaseURLDanBearer(t *testing.T) {
	var (
		path  string
		auth  string
		model string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		auth = r.Header.Get("Authorization")

		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		model = body.Model

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-uji",
			"object": "chat.completion",
			"created": 1,
			"model": "gemini-3.6-flash",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "halo"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		ChatProvider: "openai",
		ChatBaseURL:  srv.URL + "/v1",
		ChatAPIKey:   "sk-uji",
		ChatModel:    "gemini-3.6-flash",
	}

	// genaiClient sengaja nil: cabang OpenAI tidak boleh menyentuh Gemini.
	// Kalau ia menyentuh, test ini akan panik alih-alih lulus.
	cm, err := newChatModel(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("newChatModel() gagal: %v", err)
	}

	out, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("hai")})
	if err != nil {
		t.Fatalf("Generate() gagal: %v", err)
	}

	if path != "/v1/chat/completions" {
		t.Errorf("path = %q, mau %q", path, "/v1/chat/completions")
	}
	if auth != "Bearer sk-uji" {
		t.Errorf("Authorization = %q, mau %q", auth, "Bearer sk-uji")
	}
	if model != "gemini-3.6-flash" {
		t.Errorf("model terkirim = %q, mau %q", model, "gemini-3.6-flash")
	}
	if out == nil || out.Content != "halo" {
		t.Errorf("jawaban = %+v, mau Content %q", out, "halo")
	}
}

// TestChatModelOpenAIBaseURLDiteruskanApaAdanya mengunci bahwa BaseURL tidak
// "dibetulkan" diam-diam dengan menambahkan "/v1".
//
// Klien OpenAI hanya menambahkan path relatif ke BaseURL, jadi versi API harus
// sudah ada di nilai konfigurasi. Membiarkannya apa adanya membuat pengguna
// bisa menunjuk proxy dengan prefix apa pun.
func TestChatModelOpenAIBaseURLDiteruskanApaAdanya(t *testing.T) {
	var path string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "x", "object": "chat.completion", "created": 1, "model": "m",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]
		}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		ChatProvider: "openai",
		ChatBaseURL:  srv.URL, // sengaja tanpa "/v1"
		ChatAPIKey:   "sk-uji",
		ChatModel:    "m",
	}

	cm, err := newChatModel(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("newChatModel() gagal: %v", err)
	}
	if _, err := cm.Generate(context.Background(), []*schema.Message{schema.UserMessage("hai")}); err != nil {
		t.Fatalf("Generate() gagal: %v", err)
	}

	if path != "/chat/completions" {
		t.Errorf("path = %q, mau %q (BaseURL diteruskan apa adanya)",
			path, "/chat/completions")
	}
}

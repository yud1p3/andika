package rag

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"

	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
	"rag-eino/internal/gemini"
)

// Penjawab adalah kontrak jalur baca: menjawab satu giliran percakapan.
//
// Dipenuhi oleh *Chain (jalur linear) dan *Agen (jalur ReAct). Lapisan web dan
// CLI bergantung pada kontrak ini, bukan pada salah satu tipe konkretnya,
// sehingga memilih jalur cukup dilakukan sekali saat perakitan.
type Penjawab interface {
	Ask(ctx context.Context, p Pertanyaan) (string, error)
	Stream(ctx context.Context, p Pertanyaan) (*schema.StreamReader[string], error)
}

// NewPenjawab merakit jalur baca sesuai AGENT_MODE.
//
//	AGENT_MODE=false (bawaan) -> chain linear, perilaku lama tanpa perubahan
//	AGENT_MODE=true           -> agen ReAct, dengan chain linear sebagai cadangan
//
// Chain linear selalu dirakit lebih dulu karena dua alasan: ia dipakai apa
// adanya saat mode agen mati, dan ia menjadi cadangan saat agen gagal. Keduanya
// memakai retriever dasar yang sama; hanya pembungkusnya yang berbeda.
func NewPenjawab(ctx context.Context, cfg *config.Config, ai *gemini.Client, qc *qdrantclient.Client) (Penjawab, error) {
	dasar, err := newRetrieverDasar(ctx, cfg, ai, qc)
	if err != nil {
		return nil, err
	}
	rp := newRetrieverPasal(dasar)

	chain, err := newDenganRetriever(ctx, ai, rp)
	if err != nil {
		return nil, err
	}
	if !cfg.AgentMode {
		return chain, nil
	}

	// Agen memakai retriever dasar untuk tool vektor dan retriever per-pasal
	// untuk tool baca_pasal/ikuti_rujukan — lihat pencariAgen.
	agen, err := newAgenDenganPencari(ctx, ai.ChatModel, newPencariAgen(dasar, rp), cfg.AgentMaxStep)
	if err != nil {
		return nil, err
	}

	return &penjawabCadangan{utama: agen, cadangan: chain}, nil
}

// penjawabCadangan mencoba jalur agen, dan jatuh ke chain linear bila agen gagal
// sebelum sempat mengeluarkan potongan jawaban pertama.
//
// Ini menerapkan prinsip yang sama dengan fitur saran: jalur tambahan tidak
// boleh membuat pengguna kehilangan jawaban. Kegagalan yang masih bisa ditarik
// (sebelum ada teks terkirim ke layar) ditutup dengan menjawab lewat jalur
// linear; kegagalan setelah teks terkirim tidak bisa ditarik dan diteruskan apa
// adanya.
type penjawabCadangan struct {
	utama    Penjawab
	cadangan Penjawab
}

func (c *penjawabCadangan) Ask(ctx context.Context, p Pertanyaan) (string, error) {
	hasil, err := c.utama.Ask(ctx, p)
	if err == nil && strings.TrimSpace(hasil) != "" {
		return hasil, nil
	}
	if err == nil {
		err = errors.New("agen mengembalikan jawaban kosong")
	}

	log.Printf("rag: jalur agen gagal (%v), memakai jalur linear", err)
	return c.cadangan.Ask(ctx, p)
}

func (c *penjawabCadangan) Stream(ctx context.Context, p Pertanyaan) (*schema.StreamReader[string], error) {
	// Callback agen ditangkap dulu, bukan diteruskan langsung: kalau agen gagal
	// di tengah jalan dan jawabannya diambil alih jalur linear, callback agen
	// yang sudah menyala tidak boleh terlanjur mengirim rujukan yang salah ke
	// layar. Baru setelah potongan pertama terbukti ada, keduanya diteruskan.
	var (
		sumberTertunda  []string
		konteksTertunda string
	)
	pAgen := p
	pAgen.OnSumber = func(s []string) { sumberTertunda = s }
	pAgen.OnKonteks = func(k string) { konteksTertunda = k }

	sr, err := c.utama.Stream(ctx, pAgen)
	if err != nil {
		log.Printf("rag: agen gagal mulai (%v), memakai jalur linear", err)
		return c.cadangan.Stream(ctx, p)
	}

	pertama, err := sr.Recv()
	if err != nil {
		sr.Close()
		log.Printf("rag: agen gagal sebelum potongan pertama (%v), memakai jalur linear", err)
		return c.cadangan.Stream(ctx, p)
	}

	if p.OnKonteks != nil {
		p.OnKonteks(konteksTertunda)
	}
	if p.OnSumber != nil {
		p.OnSumber(sumberTertunda)
	}

	return pipaDenganDepan(pertama, sr), nil
}

// pipaDenganDepan mengembalikan aliran baru yang dimulai dengan potongan
// pertama, lalu melanjutkan sisa aliran asalnya.
//
// Diperlukan karena keputusan "agen berhasil atau tidak" baru bisa diambil
// setelah potongan pertama dibaca — dan potongan itu sudah terlanjur keluar dari
// aliran asal. Ia dikembalikan ke depan, bukan dibuang.
//
// Aliran asal ditutup di dalam goroutine ini, jadi pemanggil cukup menutup
// aliran yang dikembalikan.
func pipaDenganDepan(pertama string, sr *schema.StreamReader[string]) *schema.StreamReader[string] {
	out, tulis := schema.Pipe[string](1)

	go func() {
		defer sr.Close()
		defer tulis.Close()

		if tulis.Send(pertama, nil) {
			return
		}
		for {
			potongan, err := sr.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					tulis.Send("", err)
				}
				return
			}
			if tulis.Send(potongan, nil) {
				return
			}
		}
	}()

	return out
}

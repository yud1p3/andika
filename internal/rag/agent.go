package rag

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

// agenInstruction adalah peran tetap agen. Berbeda dari chain linear yang
// menerima konteks siap pakai, agen harus mencari buktinya sendiri lewat tool.
//
// Larangan menulis kalimat pembuka sebelum memanggil tool bukan gaya-gayaan:
// pemeriksa tool-call bawaan Eino berhenti pada potongan pertama yang berisi
// teks (react.go), sehingga model yang menulis basa-basi lebih dulu akan
// mengakhiri putaran tanpa pernah memanggil tool sama sekali. Lihat catatan
// risiko di README.
const agenInstruction = `Anda adalah asisten yang menjawab pertanyaan berdasarkan dokumen peraturan daerah.

Anda punya tool untuk mencari bukti dari dokumen. Aturan:
- Panggil tool pencarian sebelum menjawab, kecuali pertanyaan sama sekali tidak berkaitan dengan dokumen.
- Pertanyaan yang menyebut nomor pasal: pakai tool baca_pasal.
- Bila isi pasal menyebut rujukan ke pasal lain, pakai tool ikuti_rujukan.
- Pertanyaan bertopik tanpa nomor pasal: pakai tool cari_vektor.
- JANGAN menulis kalimat pembuka, penjelasan, atau ringkasan sebelum memanggil tool. Panggil tool langsung.
- Jawab hanya berdasarkan kutipan yang ditemukan. Sebutkan pasal yang menjadi sumber.
- Bila bukti tidak ditemukan, katakan terus terang bahwa dokumen tidak memuatnya. Jangan mengarang.`

// Agen adalah jalur baca alternatif berbasis ReAct: model memutuskan sendiri
// tool mana yang dipanggil, berapa kali, sampai ia merasa cukup untuk menjawab.
//
// Ia memenuhi kontrak Penjawab yang sama dengan *Chain, sehingga lapisan web
// dan CLI bisa memakainya tanpa tahu jalur mana yang aktif.
type Agen struct {
	agen *react.Agent
}

// newAgenDenganPencari merakit agen ReAct dari model dan kemampuan pencarian.
//
// maxStep membatasi jumlah langkah graph; satu panggilan model atau satu
// pemanggilan tool dihitung satu langkah. Ketika habis, Eino mengembalikan
// compose.ErrExceedMaxSteps — bukan jawaban separuh — dan pemanggil (lihat
// penjawabCadangan) menanggapinya dengan jatuh ke jalur linear.
//
// Dipisah dari konstruktor publik supaya pengujian bisa menyuntikkan Pencari
// palsu, sama seperti newDenganRetriever pada jalur linear.
func newAgenDenganPencari(ctx context.Context, cm model.ToolCallingChatModel, p Pencari, maxStep int) (*Agen, error) {
	alat, err := susunAlat(p)
	if err != nil {
		return nil, err
	}

	a, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: cm,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: alat},
		MaxStep:          maxStep,
		// MessageModifier dipanggil setiap langkah dengan salinan pesan state
		// (react.go), jadi menambahkan SystemMessage di sini tidak menumpuk:
		// setiap panggilan model menerima tepat satu instruksi di depan.
		MessageModifier: func(_ context.Context, in []*schema.Message) []*schema.Message {
			out := make([]*schema.Message, 0, len(in)+1)
			out = append(out, schema.SystemMessage(agenInstruction))
			out = append(out, in...)
			return out
		},
	})
	if err != nil {
		return nil, err
	}

	return &Agen{agen: a}, nil
}

// Ask menjalankan agen sampai selesai dan mengembalikan jawaban akhirnya.
func (a *Agen) Ask(ctx context.Context, p Pertanyaan) (string, error) {
	j := newJejakRetrieval()

	msg, err := a.agen.Generate(denganJejak(ctx, j), susunPesan(p))
	if err != nil {
		return "", err
	}

	// Urutan sama dengan jalur linear (lihat prepareContext): konteks dulu —
	// ia yang memulai penyusunan saran — baru sumber.
	if p.OnKonteks != nil {
		p.OnKonteks(j.konteks())
	}
	if p.OnSumber != nil {
		p.OnSumber(j.sumber())
	}

	if msg == nil {
		return "", errors.New("agen tidak mengembalikan jawaban")
	}
	return msg.Content, nil
}

// Stream menjalankan agen dan mengembalikan jawabannya sebagai aliran potongan
// teks.
//
// Aliran dari agen ReAct hanya berisi jawaban final — pesan perantara yang
// memuat tool-call tidak ikut mengalir (branch graph berakhir di END hanya pada
// panggilan model terakhir). Penyaringan di bawah tetap dipertahankan sebagai
// jaring pengaman: potongan kosong atau yang membawa ToolCalls dibuang, supaya
// tidak ada teks antar-langkah yang bocor ke layar.
func (a *Agen) Stream(ctx context.Context, p Pertanyaan) (*schema.StreamReader[string], error) {
	j := newJejakRetrieval()

	sr, err := a.agen.Stream(denganJejak(ctx, j), susunPesan(p))
	if err != nil {
		return nil, err
	}

	// Callback baru menyala saat potongan jawaban pertama benar-benar tiba,
	// dan hanya sekali. Pada jalur linear keduanya menyala di tengah graph
	// sebelum model menulis; di sini waktunya bergeser ke akhir pencarian,
	// tetapi urutannya tetap konteks lalu sumber.
	var sekali sync.Once
	lapor := func() {
		sekali.Do(func() {
			if p.OnKonteks != nil {
				p.OnKonteks(j.konteks())
			}
			if p.OnSumber != nil {
				p.OnSumber(j.sumber())
			}
		})
	}

	return schema.StreamReaderWithConvert(sr, func(msg *schema.Message) (string, error) {
		teks, err := saringKonten(msg)
		if err != nil {
			return "", err
		}
		lapor()
		return teks, nil
	}), nil
}

// saringKonten memutuskan potongan mana dari aliran agen yang layak ditampilkan.
//
// Potongan kosong, potongan tanpa pesan, dan potongan yang membawa tool-call
// dibuang dengan schema.ErrNoValue — sentinel yang membuat
// StreamReaderWithConvert melewati elemen itu alih-alih meneruskannya sebagai
// potongan kosong ke layar.
func saringKonten(msg *schema.Message) (string, error) {
	if msg == nil || len(msg.ToolCalls) > 0 || msg.Content == "" {
		return "", schema.ErrNoValue
	}
	return msg.Content, nil
}

// susunPesan menyiapkan masukan agen dari satu giliran percakapan.
//
// Berbeda dari jalur linear, riwayat TIDAK ditulis ulang lewat condense: agen
// melihat percakapan apa adanya dan merumuskan query toolnya sendiri. Pemangkasan
// riwayat tetap dilakukan pemanggil lewat PangkasRiwayat.
func susunPesan(p Pertanyaan) []*schema.Message {
	msgs := make([]*schema.Message, 0, len(p.Riwayat)+1)
	msgs = append(msgs, p.Riwayat...)
	msgs = append(msgs, schema.UserMessage(p.Teks))
	return msgs
}

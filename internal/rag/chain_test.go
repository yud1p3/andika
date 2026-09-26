package rag

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
	"rag-eino/internal/gemini"
)

// fakeChatModel adalah chat model palsu untuk pengujian.
//
// Berbasis pointer karena mencatat setiap panggilan. Test perlu tahu berapa
// kali Generate dipanggil dan dengan pesan apa, untuk membedakan panggilan
// penulisan ulang pertanyaan dari panggilan penyusun jawaban.
//
// Panggilan yang pesan pertamanya berisi condenseInstruction diperlakukan
// sebagai panggilan penulisan ulang dan dibalas condenseReply; panggilan yang
// pesan pertamanya berawalan saranInstructionAwal dibalas saranReply;
// panggilan lain dibalas generateReply. Kalau chunks diisi, Stream
// mengembalikannya sebagai aliran terpisah satu per satu.
type fakeChatModel struct {
	chunks []string

	condenseReply string
	condenseErr   error

	generateReply string
	generateErr   error

	saranReply string
	saranErr   error

	// panggilan mencatat seluruh pesan yang diterima Generate, terurut waktu.
	panggilan [][]*schema.Message
}

func (f *fakeChatModel) Generate(_ context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.panggilan = append(f.panggilan, in)

	if len(in) > 0 && in[0].Content == condenseInstruction {
		if f.condenseErr != nil {
			return nil, f.condenseErr
		}
		return schema.AssistantMessage(f.condenseReply, nil), nil
	}

	// Dicek sebelum cabang generate, supaya test saran tidak ikut dibalas
	// generateReply.
	if len(in) > 0 && strings.HasPrefix(in[0].Content, saranInstructionAwal) {
		if f.saranErr != nil {
			return nil, f.saranErr
		}
		return schema.AssistantMessage(f.saranReply, nil), nil
	}

	if f.generateErr != nil {
		return nil, f.generateErr
	}
	return schema.AssistantMessage(f.generateReply, nil), nil
}

func (f *fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msgs := make([]*schema.Message, 0, len(f.chunks))
	for _, c := range f.chunks {
		msgs = append(msgs, schema.AssistantMessage(c, nil))
	}
	return schema.StreamReaderFromArray(msgs), nil
}

func (f *fakeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}

// jumlahCondense menghitung berapa kali model diminta menulis ulang pertanyaan.
func (f *fakeChatModel) jumlahCondense() int {
	n := 0
	for _, in := range f.panggilan {
		if len(in) > 0 && in[0].Content == condenseInstruction {
			n++
		}
	}
	return n
}

// jumlahSaran menghitung berapa kali model diminta menyusun saran lanjutan.
func (f *fakeChatModel) jumlahSaran() int {
	n := 0
	for _, in := range f.panggilan {
		if len(in) > 0 && strings.HasPrefix(in[0].Content, saranInstructionAwal) {
			n++
		}
	}
	return n
}

// pesanTerakhir mengembalikan pesan yang diterima pada panggilan terakhir,
// yaitu panggilan penyusun jawaban.
func (f *fakeChatModel) pesanTerakhir(t *testing.T) []*schema.Message {
	t.Helper()

	if len(f.panggilan) == 0 {
		t.Fatal("model tidak pernah dipanggil")
	}
	return f.panggilan[len(f.panggilan)-1]
}

type fakeEmbedder struct{}

func (fakeEmbedder) EmbedStrings(_ context.Context, texts []string, _ ...embedding.Option) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = make([]float64, 768)
	}
	return out, nil
}

// fakeRetriever mengembalikan dokumen tetap tanpa menyentuh Qdrant, dan
// mencatat query yang diterimanya.
//
// Pencatatan itu yang membuktikan retrieval memakai pertanyaan hasil penulisan
// ulang, bukan pertanyaan asli pengguna.
type fakeRetriever struct {
	docs    []*schema.Document
	queries []string
}

func (f *fakeRetriever) Retrieve(_ context.Context, query string, _ ...retriever.Option) ([]*schema.Document, error) {
	f.queries = append(f.queries, query)
	return f.docs, nil
}

// newTestChain merakit chain memakai komponen palsu, sehingga tidak butuh API
// key Gemini maupun server Qdrant yang berjalan.
func newTestChain(t *testing.T, cm model.ToolCallingChatModel, ret retriever.Retriever) *Chain {
	t.Helper()

	ai := &gemini.Client{
		ChatModel:     cm,
		DocEmbedder:   fakeEmbedder{},
		QueryEmbedder: fakeEmbedder{},
	}

	chain, err := newDenganRetriever(context.Background(), ai, ret)
	if err != nil {
		t.Fatalf("merakit chain gagal: %v", err)
	}
	return chain
}

// docsUji adalah dokumen tetap yang dikembalikan fakeRetriever.
func docsUji() []*schema.Document {
	return []*schema.Document{{ID: "d1", Content: "MPASI diberikan mulai usia 6 bulan."}}
}

// TestChainWiringCompiles memastikan rangkaian node di chain RAG tersambung
// dengan tipe yang benar.
//
// Ini perlu diuji terpisah karena go build tidak bisa menangkap kesalahan
// wiring antar-node: AddLambdaNode menerima tipe *compose.Lambda yang tidak
// membawa informasi tipe, sehingga ketidakcocokan tipe antar node baru
// terdeteksi saat graph di-Compile, yaitu ketika program sudah dijalankan.
func TestChainWiringCompiles(t *testing.T) {
	qc, err := qdrantclient.NewClient(&qdrantclient.Config{Host: "localhost", Port: 6334})
	if err != nil {
		t.Fatalf("qdrant client: %v", err)
	}
	defer qc.Close()

	ai := &gemini.Client{
		ChatModel:     &fakeChatModel{},
		DocEmbedder:   fakeEmbedder{},
		QueryEmbedder: fakeEmbedder{},
	}

	cfg := &config.Config{QdrantCollection: "test", TopK: 5}

	if _, err := New(context.Background(), cfg, ai, qc); err != nil {
		t.Fatalf("merakit chain gagal: %v", err)
	}
}

// TestChainStreamMengalirBertahap memastikan jawaban benar-benar mengalir per
// potongan, bukan tertahan sampai model selesai.
//
// Ini penjaga regresi untuk jebakan di node "parse". Kalau node itu memakai
// InvokableLambda, Eino menurunkan fungsi transform-nya lewat transformByInvoke
// yang lebih dulu mengumpulkan seluruh aliran menjadi satu nilai — sehingga
// tiga potongan di bawah akan keluar sebagai SATU potongan dan test ini gagal.
//
// Node "condense" dan "prepare" memang InvokableLambda, tapi keduanya berada di
// depan model dan hanya meneruskan satu nilai, jadi aliran model sampai
// pemanggil tetap hidup.
func TestChainStreamMengalirBertahap(t *testing.T) {
	chain := newTestChain(t, &fakeChatModel{chunks: []string{"satu ", "dua ", "tiga"}}, &fakeRetriever{docs: docsUji()})

	sr, err := chain.Stream(context.Background(), Pertanyaan{Teks: "kapan MPASI mulai diberikan?"})
	if err != nil {
		t.Fatalf("menjalankan Stream: %v", err)
	}
	defer sr.Close()

	var potongan []string
	for {
		chunk, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("menerima potongan: %v", err)
		}
		potongan = append(potongan, chunk)
	}

	if len(potongan) != 3 {
		t.Fatalf("dapat %d potongan %q, mau 3 — aliran kemungkinan ter-buffer di node parse",
			len(potongan), potongan)
	}
	if got := strings.Join(potongan, ""); got != "satu dua tiga" {
		t.Errorf("gabungan potongan = %q, mau %q", got, "satu dua tiga")
	}
}

// TestChainAskTetapBekerja memastikan mode Invoke tidak rusak oleh perubahan
// node parse menjadi TransformableLambda.
//
// Eino menyediakan fallback invokeByTransform untuk node yang hanya punya
// fungsi transform, jadi Ask seharusnya tetap mengembalikan satu string utuh.
func TestChainAskTetapBekerja(t *testing.T) {
	chain := newTestChain(t,
		&fakeChatModel{generateReply: "ok"},
		&fakeRetriever{docs: docsUji()})

	jawaban, err := chain.Ask(context.Background(), Pertanyaan{Teks: "kapan MPASI mulai diberikan?"})
	if err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}
	if jawaban != "ok" {
		t.Errorf("jawaban = %q, mau %q", jawaban, "ok")
	}
}

// TestCondenseMenulisUlangPertanyaan adalah inti fitur memori: pertanyaan
// lanjutan yang menggantung harus ditulis ulang jadi pertanyaan mandiri untuk
// keperluan retrieval, sementara model tetap menerima pertanyaan asli.
func TestCondenseMenulisUlangPertanyaan(t *testing.T) {
	const (
		asli    = "kalau yang poin a di atas itu apa?"
		mandiri = "Apa saja jenis naskah dinas pengaturan?"
	)

	cm := &fakeChatModel{condenseReply: mandiri, generateReply: "ok"}
	ret := &fakeRetriever{docs: []*schema.Document{{ID: "d1", Content: "Naskah dinas bersifat mengatur."}}}

	chain := newTestChain(t, cm, ret)

	riwayat := []*schema.Message{
		schema.UserMessage("sebutkan jenis naskah dinas"),
		schema.AssistantMessage("a. naskah dinas pengaturan\nb. naskah dinas penetapan", nil),
	}

	if _, err := chain.Ask(context.Background(), Pertanyaan{Teks: asli, Riwayat: riwayat}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	// Retrieval harus memakai pertanyaan hasil penulisan ulang. Kalau yang
	// dipakai pertanyaan asli, vector store tidak akan menemukan apa pun yang
	// berguna karena "poin a di atas" tidak ada di dokumen.
	if len(ret.queries) != 1 {
		t.Fatalf("retriever dipanggil %d kali, mau 1", len(ret.queries))
	}
	if ret.queries[0] != mandiri {
		t.Errorf("query retrieval = %q, mau %q", ret.queries[0], mandiri)
	}

	if n := cm.jumlahCondense(); n != 1 {
		t.Fatalf("penulisan ulang dipanggil %d kali, mau 1", n)
	}

	jawab := cm.pesanTerakhir(t)

	// Prompt harus memuat pertanyaan ASLI: pengguna harus melihat jawaban atas
	// apa yang benar-benar ia tanyakan.
	if !pesanMemuat(jawab, asli) {
		t.Errorf("prompt tidak memuat pertanyaan asli %q:\n%v", asli, isiPesan(jawab))
	}
	if pesanMemuat(jawab, mandiri) {
		t.Errorf("prompt memuat pertanyaan hasil penulisan ulang %q; seharusnya hanya pertanyaan asli:\n%v",
			mandiri, isiPesan(jawab))
	}

	// Riwayat harus ikut sebagai pesan tersendiri, bukan dirangkai jadi teks.
	if !pesanMemuat(jawab, "a. naskah dinas pengaturan") {
		t.Errorf("prompt tidak memuat riwayat:\n%v", isiPesan(jawab))
	}
}

// TestTanpaRiwayatTidakMemanggilCondense memastikan giliran pertama tidak
// memboroskan satu panggilan model.
//
// Selain menghemat kuota, ini juga yang membuat perilaku tanpa memori persis
// sama seperti sebelum fitur ini ada.
func TestTanpaRiwayatTidakMemanggilCondense(t *testing.T) {
	cm := &fakeChatModel{condenseReply: "tidak boleh dipakai", generateReply: "ok"}
	ret := &fakeRetriever{docs: docsUji()}

	chain := newTestChain(t, cm, ret)

	if _, err := chain.Ask(context.Background(), Pertanyaan{Teks: "kapan MPASI mulai?"}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if n := cm.jumlahCondense(); n != 0 {
		t.Errorf("penulisan ulang dipanggil %d kali tanpa riwayat, mau 0", n)
	}
	if len(cm.panggilan) != 1 {
		t.Errorf("model dipanggil %d kali, mau 1 (hanya menyusun jawaban)", len(cm.panggilan))
	}
	if got := ret.queries[0]; got != "kapan MPASI mulai?" {
		t.Errorf("query retrieval = %q, mau pertanyaan asli", got)
	}
}

// TestTanpaRiwayatTidakMemasangKunciHistory memaku perilaku kunci "history"
// saat riwayat kosong.
//
// MessagesPlaceholder melakukan type assertion ke []*schema.Message. Nilai
// bertipe lain — termasuk nil tanpa tipe — ditolak dengan "only messages can
// be used to format message placeholder, key: history, actual type: <nil>",
// dan flag optional=true tidak menolong karena kuncinya memang ada. Karena itu
// kuncinya harus benar-benar dihilangkan, bukan diisi nil.
//
// (Nil slice bertipe []*schema.Message sendiri aman; yang berbahaya adalah
// nilai yang tipe dinamisnya bukan itu.)
func TestTanpaRiwayatTidakMemasangKunciHistory(t *testing.T) {
	chain := newTestChain(t,
		&fakeChatModel{generateReply: "ok"},
		&fakeRetriever{docs: docsUji()})

	kasus := []struct {
		nama    string
		riwayat []*schema.Message
	}{
		{"tanpa riwayat", nil},
		{"riwayat kosong", []*schema.Message{}},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			if _, err := chain.Ask(context.Background(), Pertanyaan{Teks: "kapan MPASI?", Riwayat: k.riwayat}); err != nil {
				t.Fatalf("Ask gagal: %v", err)
			}
		})
	}
}

// TestCondenseGagalJatuhKePertanyaanAsli memastikan penulisan ulang yang gagal
// tidak menggagalkan seluruh jawaban.
//
// Dua jalur diuji karena keduanya sama-sama berbahaya: error (kuota habis,
// jaringan putus) dan hasil kosong. Hasil kosong tidak memicu error apa pun,
// tapi query kosong akan mengembalikan chunk acak dari Qdrant — lebih buruk
// daripada memakai pertanyaan asli.
func TestCondenseGagalJatuhKePertanyaanAsli(t *testing.T) {
	const asli = "poin a di atas apa?"

	kasus := []struct {
		nama string
		cm   *fakeChatModel
	}{
		{"error", &fakeChatModel{condenseErr: errors.New("kuota habis"), generateReply: "ok"}},
		{"hasil kosong", &fakeChatModel{condenseReply: "   ", generateReply: "ok"}},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			ret := &fakeRetriever{docs: docsUji()}
			chain := newTestChain(t, k.cm, ret)

			riwayat := []*schema.Message{schema.UserMessage("sebelumnya")}

			jawaban, err := chain.Ask(context.Background(), Pertanyaan{Teks: asli, Riwayat: riwayat})
			if err != nil {
				t.Fatalf("menjalankan Ask: %v", err)
			}
			if jawaban != "ok" {
				t.Errorf("jawaban = %q, mau %q", jawaban, "ok")
			}

			if len(ret.queries) != 1 || ret.queries[0] != asli {
				t.Errorf("query retrieval = %q, mau pertanyaan asli %q", ret.queries, asli)
			}
		})
	}
}

// TestPangkasRiwayat menjaga batas HISTORY_MESSAGES.
func TestPangkasRiwayat(t *testing.T) {
	pesan := func(n int) []*schema.Message {
		out := make([]*schema.Message, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, schema.UserMessage(strconv.Itoa(i)))
		}
		return out
	}

	// 0 dan negatif berarti memori dimatikan, bukan "tanpa batas". Satu arti
	// untuk satu nilai, supaya HISTORY_MESSAGES=0 tidak diam-diam berarti
	// sebaliknya.
	for _, maks := range []int{0, -1} {
		if got := PangkasRiwayat(pesan(4), maks); got != nil {
			t.Errorf("PangkasRiwayat(maks=%d) = %d pesan, mau nil (memori mati)", maks, len(got))
		}
	}

	// Riwayat yang lebih pendek dari batas dibiarkan utuh.
	if got := PangkasRiwayat(pesan(2), 6); len(got) != 2 {
		t.Errorf("riwayat pendek ikut terpotong: %d pesan, mau 2", len(got))
	}

	// Dipotong dari depan: yang tersisa harus pesan paling akhir.
	got := PangkasRiwayat(pesan(10), 3)
	if len(got) != 3 {
		t.Fatalf("dapat %d pesan, mau 3", len(got))
	}
	for i, m := range got {
		want := strconv.Itoa(8 + i)
		if m.Content != want {
			t.Errorf("pesan ke-%d = %q, mau %q (harus dipotong dari depan)", i, m.Content, want)
		}
	}
}

// TestPrepareMengirimKonteks memastikan OnKonteks menerima konteks penuh yang
// sama dengan yang diisi ke placeholder {context}.
//
// Fitur saran bergantung pada ini: ia menyusun pertanyaan lanjutan dari
// potongan yang benar-benar dipakai menjawab, bukan dari teks jawaban.
func TestPrepareMengirimKonteks(t *testing.T) {
	docs := []*schema.Document{
		{ID: "d1", Content: "Pasal 7 mengatur jenis naskah dinas."},
		{ID: "d2", Content: "Naskah dinas pengaturan bersifat mengikat."},
	}
	cm := &fakeChatModel{generateReply: "ok"}

	var (
		dapat  []string
		jumlah int
	)
	chain := newTestChain(t, cm, &fakeRetriever{docs: docs})

	if _, err := chain.Ask(context.Background(), Pertanyaan{
		Teks: "apa saja jenis naskah dinas?",
		OnKonteks: func(k string) {
			jumlah++
			dapat = append(dapat, k)
		},
	}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if jumlah != 1 {
		t.Fatalf("OnKonteks dipanggil %d kali, mau 1", jumlah)
	}
	if mau := formatDocs(docs); dapat[0] != mau {
		t.Errorf("konteks = %q, mau %q", dapat[0], mau)
	}

	// Konteks yang sama juga harus sampai ke prompt.
	if !pesanMemuat(cm.pesanTerakhir(t), "Naskah dinas pengaturan bersifat mengikat.") {
		t.Errorf("prompt tidak memuat konteks:\n%v", isiPesan(cm.pesanTerakhir(t)))
	}
}

// TestOnKonteksDipanggilSebelumOnSumber memaku urutan dua callback itu.
//
// OnKonteks memulai penyusunan saran di latar belakang, sedangkan OnSumber
// menulis ke respons HTTP yang bisa memblokir pada klien lambat. Kalau
// urutannya terbalik, saran baru mulai setelah klien selesai menerima — persis
// penundaan yang mau dihindari.
func TestOnKonteksDipanggilSebelumOnSumber(t *testing.T) {
	var urutan []string
	chain := newTestChain(t,
		&fakeChatModel{generateReply: "ok"},
		&fakeRetriever{docs: docsUji()})

	if _, err := chain.Ask(context.Background(), Pertanyaan{
		Teks:      "kapan MPASI mulai?",
		OnKonteks: func(string) { urutan = append(urutan, "konteks") },
		OnSumber:  func([]string) { urutan = append(urutan, "sumber") },
	}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if len(urutan) != 2 || urutan[0] != "konteks" || urutan[1] != "sumber" {
		t.Errorf("urutan callback = %v, mau [konteks sumber]", urutan)
	}
}

// TestTanpaOnKonteksTidakPanik menjaga agar kedua callback tetap opsional.
func TestTanpaOnKonteksTidakPanik(t *testing.T) {
	chain := newTestChain(t,
		&fakeChatModel{generateReply: "ok"},
		&fakeRetriever{docs: docsUji()})

	if _, err := chain.Ask(context.Background(), Pertanyaan{Teks: "kapan MPASI?"}); err != nil {
		t.Fatalf("Ask tanpa callback gagal: %v", err)
	}
}

// TestOnKonteksIkutSaatCondenseMenulisUlang memastikan callback tidak hilang
// saat riwayat ada.
//
// condenseQuestion membangun struct langkah baru di dua jalur (langsung dan
// hasil penulisan ulang). Kalau salah satu lupa menyalin OnKonteks, fitur saran
// mati diam-diam hanya pada percakapan lanjutan — persis kasus yang paling
// butuh saran.
func TestOnKonteksIkutSaatCondenseMenulisUlang(t *testing.T) {
	cm := &fakeChatModel{condenseReply: "Apa saja jenis naskah dinas?", generateReply: "ok"}
	chain := newTestChain(t, cm, &fakeRetriever{docs: docsUji()})

	dipanggil := 0
	riwayat := []*schema.Message{schema.UserMessage("sebelumnya")}

	if _, err := chain.Ask(context.Background(), Pertanyaan{
		Teks:      "poin a di atas apa?",
		Riwayat:   riwayat,
		OnKonteks: func(string) { dipanggil++ },
	}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if dipanggil != 1 {
		t.Errorf("OnKonteks dipanggil %d kali setelah condense, mau 1", dipanggil)
	}
}

// pesanMemuat melaporkan apakah ada satu pesan yang memuat teks tertentu.
func pesanMemuat(msgs []*schema.Message, teks string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, teks) {
			return true
		}
	}
	return false
}

// isiPesan menyiapkan isi pesan dalam bentuk yang enak dibaca saat test gagal.
func isiPesan(msgs []*schema.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, string(m.Role)+": "+m.Content)
	}
	return out
}

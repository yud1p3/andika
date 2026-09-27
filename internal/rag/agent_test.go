package rag

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// fakeAgenModel adalah model palsu berskenario untuk menguji agen.
//
// Berbeda dari fakeChatModel yang membedakan panggilan dari isi pesan, model ini
// mengembalikan balasan menurut urutan panggilan: langkah[0] untuk panggilan
// pertama, langkah[1] untuk yang kedua, dan seterusnya. Itulah bentuk yang
// dibutuhkan agen, karena setiap putaran bergantung pada hasil putaran
// sebelumnya — bukan pada jenis pertanyaan.
//
// Satu langkah berupa daftar potongan supaya jalur streaming bisa diuji: model
// yang sama mengembalikan satu pesan utuh lewat Generate, dan potongan-potongan
// itu lewat Stream.
type fakeAgenModel struct {
	mu sync.Mutex

	// langkah adalah balasan per panggilan model, terurut waktu.
	langkah [][]*schema.Message
	// setelahHabis dipakai untuk panggilan melewati panjang langkah. Nil berarti
	// membalas "selesai"; diisi pesan tool-call berarti model tidak pernah
	// berhenti, yang diperlukan untuk menguji batas langkah.
	setelahHabis []*schema.Message

	err       error
	panggilan int
}

func (f *fakeAgenModel) berikutnya() ([]*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}

	var potongan []*schema.Message
	switch {
	case f.panggilan < len(f.langkah):
		potongan = f.langkah[f.panggilan]
	case f.setelahHabis != nil:
		potongan = f.setelahHabis
	default:
		potongan = []*schema.Message{schema.AssistantMessage("selesai", nil)}
	}
	f.panggilan++

	return potongan, nil
}

func (f *fakeAgenModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	potongan, err := f.berikutnya()
	if err != nil {
		return nil, err
	}
	return schema.ConcatMessages(potongan)
}

func (f *fakeAgenModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	potongan, err := f.berikutnya()
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray(potongan), nil
}

func (f *fakeAgenModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return f, nil
}

// pesanToolCall menyusun balasan model yang meminta satu tool dipanggil.
func pesanToolCall(id, nama, argumen string) *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      nama,
			Arguments: argumen,
		},
	}})
}

// docPasal menyusun dokumen uji yang punya metadata pasal, sehingga labelSumber
// menghasilkannya sebagai "Pasal N" seperti pada dokumen sungguhan.
func docPasal(id, nomor, isi string) *schema.Document {
	return &schema.Document{
		ID:      id,
		Content: isi,
		MetaData: map[string]any{
			kunciPayloadMetadata: map[string]*qdrantclient.Value{
				kunciPasal: {Kind: &qdrantclient.Value_StringValue{StringValue: nomor}},
			},
		},
	}
}

// fakePencari menggantikan kemampuan retrieval agen, dan mencatat setiap
// permintaan supaya test bisa memastikan tool yang benar yang dipanggil.
type fakePencari struct {
	mu sync.Mutex

	vektor     []*schema.Document
	vektorErr  error
	pasal      map[string][]*schema.Document
	pasalErr   error
	rujukan    map[string][]*schema.Document
	rujukanErr error

	queries      []string
	nomorPasal   []string
	nomorRujukan []string
}

func (f *fakePencari) Vektor(_ context.Context, query string) ([]*schema.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.queries = append(f.queries, query)
	return f.vektor, f.vektorErr
}

func (f *fakePencari) Pasal(_ context.Context, nomor []string) ([]*schema.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nomorPasal = append(f.nomorPasal, nomor...)
	if f.pasalErr != nil {
		return nil, f.pasalErr
	}
	return f.pasal[nomor[0]], nil
}

func (f *fakePencari) Rujukan(_ context.Context, nomor []string) ([]*schema.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nomorRujukan = append(f.nomorRujukan, nomor...)
	if f.rujukanErr != nil {
		return nil, f.rujukanErr
	}
	return f.rujukan[nomor[0]], nil
}

// newTestAgen merakit agen memakai model dan pencari palsu, sehingga tidak butuh
// API key Gemini maupun server Qdrant.
func newTestAgen(t *testing.T, cm model.ToolCallingChatModel, p Pencari, maxStep int) *Agen {
	t.Helper()

	a, err := newAgenDenganPencari(context.Background(), cm, p, maxStep)
	if err != nil {
		t.Fatalf("merakit agen gagal: %v", err)
	}
	return a
}

// kurasStream menghabiskan aliran teks menjadi daftar potongan.
func kurasStream(t *testing.T, sr *schema.StreamReader[string]) []string {
	t.Helper()

	var potongan []string
	for {
		teks, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return potongan
		}
		if err != nil {
			t.Fatalf("menerima potongan: %v", err)
		}
		potongan = append(potongan, teks)
	}
}

// TestAgenWiringCompiles memastikan agen beserta ketiga tool-nya tersambung
// dengan tipe yang benar. Seperti chain, kesalahan wiring agen baru terdeteksi
// saat graph di-Compile.
func TestAgenWiringCompiles(t *testing.T) {
	newTestAgen(t, &fakeAgenModel{}, &fakePencari{}, 8)
}

// TestAgenMemanggilToolBacaPasal memastikan pertanyaan bernomor pasal
// mengarahkan agen ke tool baca_pasal, dan hasilnya mengalir ke callback
// rujukan serta konteks.
func TestAgenMemanggilToolBacaPasal(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{pesanToolCall("c1", "baca_pasal", `{"nomor":"7"}`)},
		{schema.AssistantMessage("Pasal 7 mengatur naskah dinas.", nil)},
	}}
	p := &fakePencari{pasal: map[string][]*schema.Document{
		"7": {docPasal("d7", "7", "Pasal 7 mengatur jenis naskah dinas.")},
	}}

	a := newTestAgen(t, cm, p, 8)

	var (
		sumber  []string
		konteks []string
	)
	jawaban, err := a.Ask(context.Background(), Pertanyaan{
		Teks:      "pasal 7 mengatur apa?",
		OnSumber:  func(s []string) { sumber = append(sumber, s...) },
		OnKonteks: func(k string) { konteks = append(konteks, k) },
	})
	if err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if jawaban != "Pasal 7 mengatur naskah dinas." {
		t.Errorf("jawaban = %q, mau %q", jawaban, "Pasal 7 mengatur naskah dinas.")
	}
	if len(p.nomorPasal) != 1 || p.nomorPasal[0] != "7" {
		t.Errorf("baca_pasal dipanggil dengan %v, mau [7]", p.nomorPasal)
	}
	if len(konteks) != 1 {
		t.Fatalf("OnKonteks dipanggil %d kali, mau 1", len(konteks))
	}
	if !strings.Contains(konteks[0], "Pasal 7 mengatur jenis naskah dinas.") {
		t.Errorf("konteks = %q, mau memuat isi pasal 7", konteks[0])
	}
	if len(sumber) != 1 || sumber[0] != "Pasal 7" {
		t.Errorf("sumber = %v, mau [Pasal 7]", sumber)
	}
}

// TestAgenMemanggilToolCariVektor memastikan pertanyaan bertopik mengarahkan
// agen ke tool cari_vektor.
func TestAgenMemanggilToolCariVektor(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{pesanToolCall("c1", "cari_vektor", `{"query":"jenis naskah dinas"}`)},
		{schema.AssistantMessage("Ada beberapa jenis naskah dinas.", nil)},
	}}
	p := &fakePencari{vektor: []*schema.Document{docPasal("d7", "7", "Naskah dinas diatur di Pasal 7.")}}

	a := newTestAgen(t, cm, p, 8)

	jawaban, err := a.Ask(context.Background(), Pertanyaan{Teks: "apa saja jenis naskah dinas?"})
	if err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}
	if jawaban != "Ada beberapa jenis naskah dinas." {
		t.Errorf("jawaban = %q", jawaban)
	}
	if len(p.queries) != 1 || p.queries[0] != "jenis naskah dinas" {
		t.Errorf("cari_vektor dipanggil dengan %v, mau [jenis naskah dinas]", p.queries)
	}
}

// TestAgenMemanggilToolIkutiRujukan memastikan pasal rujukan ikut masuk konteks.
//
// Inilah keuntungan utama agen: ia bisa menelusuri rujukan silang atas
// keputusannya sendiri, bukan hanya satu lompatan yang dipaku di kode.
func TestAgenMemanggilToolIkutiRujukan(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{pesanToolCall("c1", "ikuti_rujukan", `{"nomor":"145"}`)},
		{schema.AssistantMessage("Pasal 145 menunjuk Pasal 25.", nil)},
	}}
	p := &fakePencari{rujukan: map[string][]*schema.Document{
		"145": {
			docPasal("d145", "145", "Pasal 145 menunjuk Pasal 25."),
			docPasal("d25", "25", "Pasal 25 mengatur surat kuasa."),
		},
	}}

	a := newTestAgen(t, cm, p, 8)

	var konteks string
	if _, err := a.Ask(context.Background(), Pertanyaan{
		Teks:      "pasal 145 mengatur apa?",
		OnKonteks: func(k string) { konteks = k },
	}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if len(p.nomorRujukan) != 1 || p.nomorRujukan[0] != "145" {
		t.Errorf("ikuti_rujukan dipanggil dengan %v, mau [145]", p.nomorRujukan)
	}
	if !strings.Contains(konteks, "Pasal 25 mengatur surat kuasa.") {
		t.Errorf("konteks tidak memuat pasal rujukan:\n%s", konteks)
	}
}

// TestAgenStreamMenyaringPotongan memastikan jawaban mengalir bertahap dan
// tidak ada potongan tool-call yang bocor ke layar.
func TestAgenStreamMenyaringPotongan(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{pesanToolCall("c1", "baca_pasal", `{"nomor":"7"}`)},
		{
			schema.AssistantMessage("satu ", nil),
			schema.AssistantMessage("dua ", nil),
			schema.AssistantMessage("tiga", nil),
		},
	}}
	p := &fakePencari{pasal: map[string][]*schema.Document{
		"7": {docPasal("d7", "7", "Pasal 7 mengatur naskah dinas.")},
	}}

	a := newTestAgen(t, cm, p, 8)

	sr, err := a.Stream(context.Background(), Pertanyaan{Teks: "pasal 7 mengatur apa?"})
	if err != nil {
		t.Fatalf("menjalankan Stream: %v", err)
	}
	defer sr.Close()

	potongan := kurasStream(t, sr)
	if len(potongan) != 3 {
		t.Fatalf("dapat %d potongan %q, mau 3", len(potongan), potongan)
	}
	if got := strings.Join(potongan, ""); got != "satu dua tiga" {
		t.Errorf("gabungan potongan = %q, mau %q", got, "satu dua tiga")
	}
}

// TestSaringKontenMembuangPesanToolCall memaku perilaku penyaring: pesan yang
// membawa tool-call tidak pernah ditampilkan, bahkan bila ia juga membawa teks.
//
// Pesan antara agen memang tidak ikut mengalir ke pemanggil, tetapi penyaring
// tetap dipertahankan sebagai jaring pengaman — dan perilakunya dikunci di sini
// supaya tidak diam-diam dilonggarkan di kemudian hari.
func TestSaringKontenMembuangPesanToolCall(t *testing.T) {
	kasus := []struct {
		nama string
		msg  *schema.Message
	}{
		{"pesan nil", nil},
		{"konten kosong", schema.AssistantMessage("", nil)},
		{"membawa tool-call", pesanToolCall("c1", "baca_pasal", `{"nomor":"7"}`)},
		{"konten dan tool-call", schema.AssistantMessage("sedang mencari", []schema.ToolCall{{
			ID:       "c1",
			Type:     "function",
			Function: schema.FunctionCall{Name: "baca_pasal", Arguments: `{"nomor":"7"}`},
		}})},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			teks, err := saringKonten(k.msg)
			if !errors.Is(err, schema.ErrNoValue) {
				t.Fatalf("saringKonten() err = %v, mau schema.ErrNoValue", err)
			}
			if teks != "" {
				t.Errorf("saringKonten() teks = %q, mau kosong", teks)
			}
		})
	}

	// Konten biasa tetap lolos.
	teks, err := saringKonten(schema.AssistantMessage("jawaban", nil))
	if err != nil {
		t.Fatalf("saringKonten() konten biasa err = %v, mau nil", err)
	}
	if teks != "jawaban" {
		t.Errorf("saringKonten() = %q, mau %q", teks, "jawaban")
	}
}

// TestAgenCallbackSekaliDenganBanyakTool memastikan callback tetap menyala
// sekali walau agen retrieval berkali-kali, dan memuat gabungan buktinya.
//
// Ini yang membuat antarmuka tetap utuh: rujukan di bawah jawaban dan saran
// lanjutan dibangun dari satu kumpulan bukti, bukan dari pencarian terakhir.
func TestAgenCallbackSekaliDenganBanyakTool(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{pesanToolCall("c1", "cari_vektor", `{"query":"naskah dinas"}`)},
		{pesanToolCall("c2", "baca_pasal", `{"nomor":"9"}`)},
		{schema.AssistantMessage("jawaban gabungan", nil)},
	}}
	p := &fakePencari{
		vektor: []*schema.Document{docPasal("d7", "7", "Naskah dinas di Pasal 7.")},
		pasal: map[string][]*schema.Document{
			"9": {docPasal("d9", "9", "Pasal 9 mengatur tata cara.")},
		},
	}

	a := newTestAgen(t, cm, p, 12)

	var (
		jumlahSumber  int
		jumlahKonteks int
		sumber        []string
		konteks       string
	)
	if _, err := a.Ask(context.Background(), Pertanyaan{
		Teks:      "naskah dinas dan tata caranya?",
		OnSumber:  func(s []string) { jumlahSumber++; sumber = s },
		OnKonteks: func(k string) { jumlahKonteks++; konteks = k },
	}); err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}

	if jumlahSumber != 1 {
		t.Errorf("OnSumber dipanggil %d kali, mau 1", jumlahSumber)
	}
	if jumlahKonteks != 1 {
		t.Errorf("OnKonteks dipanggil %d kali, mau 1", jumlahKonteks)
	}

	for _, mau := range []string{"Pasal 7", "Pasal 9"} {
		if !memuat(sumber, mau) {
			t.Errorf("sumber = %v, mau memuat %q", sumber, mau)
		}
	}
	if !strings.Contains(konteks, "Naskah dinas di Pasal 7.") || !strings.Contains(konteks, "Pasal 9 mengatur tata cara.") {
		t.Errorf("konteks tidak memuat gabungan bukti kedua tool:\n%s", konteks)
	}
}

// TestAgenTanpaToolTetapMenjawab memastikan agen yang langsung menjawab tanpa
// memanggil tool tetap bekerja, dan callback tetap menyala sekali.
func TestAgenTanpaToolTetapMenjawab(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{schema.AssistantMessage("Saya tidak menemukan buktinya.", nil)},
	}}

	a := newTestAgen(t, cm, &fakePencari{}, 8)

	var jumlahSumber int
	jawaban, err := a.Ask(context.Background(), Pertanyaan{
		Teks:     "pertanyaan di luar dokumen",
		OnSumber: func([]string) { jumlahSumber++ },
	})
	if err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}
	if jawaban != "Saya tidak menemukan buktinya." {
		t.Errorf("jawaban = %q", jawaban)
	}
	if jumlahSumber != 1 {
		t.Errorf("OnSumber dipanggil %d kali, mau 1", jumlahSumber)
	}
}

// TestAgenMaxStepTerlampaui memastikan batas langkah benar-benar menghentikan
// agen yang tidak kunjung selesai, dengan error yang bisa dikenali.
//
// Inilah jaring pengaman biaya: tanpa batas ini, model yang selalu meminta tool
// akan berputar sampai kehabisan kuota.
func TestAgenMaxStepTerlampaui(t *testing.T) {
	cm := &fakeAgenModel{setelahHabis: []*schema.Message{
		pesanToolCall("c", "cari_vektor", `{"query":"apa saja"}`),
	}}
	p := &fakePencari{vektor: []*schema.Document{docPasal("d1", "1", "isi")}}

	a := newTestAgen(t, cm, p, 3)

	_, err := a.Ask(context.Background(), Pertanyaan{Teks: "apa saja?"})
	if !errors.Is(err, compose.ErrExceedMaxSteps) {
		t.Fatalf("Ask() err = %v, mau compose.ErrExceedMaxSteps", err)
	}
}

// TestAgenToolGagalTidakMenjatuhkanAgen memastikan kegagalan retrieval di dalam
// tool tidak menggagalkan seluruh percakapan.
//
// Tool mengembalikan catatan sebagai teks, bukan error Go, sehingga agen masih
// bisa menjawab atau mengaku tidak menemukan bukti. Meniru sikap yang sama
// dengan retriever per-pasal di jalur linear.
func TestAgenToolGagalTidakMenjatuhkanAgen(t *testing.T) {
	cm := &fakeAgenModel{langkah: [][]*schema.Message{
		{pesanToolCall("c1", "baca_pasal", `{"nomor":"7"}`)},
		{schema.AssistantMessage("Pencarian gagal, saya tidak bisa memastikan.", nil)},
	}}
	p := &fakePencari{pasalErr: errors.New("qdrant tidak terjangkau")}

	a := newTestAgen(t, cm, p, 8)

	jawaban, err := a.Ask(context.Background(), Pertanyaan{Teks: "pasal 7 mengatur apa?"})
	if err != nil {
		t.Fatalf("menjalankan Ask: %v", err)
	}
	if jawaban != "Pencarian gagal, saya tidak bisa memastikan." {
		t.Errorf("jawaban = %q", jawaban)
	}
}

// memuat melaporkan apakah sebuah slice memuat teks tertentu.
func memuat(slice []string, teks string) bool {
	for _, s := range slice {
		if s == teks {
			return true
		}
	}
	return false
}

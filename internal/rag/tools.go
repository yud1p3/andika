package rag

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// Pencari adalah kemampuan retrieval yang dipakai tool agen.
//
// Ketiga metode sengaja dipisah supaya agen bisa memilih langkahnya sendiri —
// itulah bedanya dengan jalur linear, yang menyaring per pasal dan mengikuti
// rujukan secara otomatis tanpa bertanya. Dipenuhi oleh pencariAgen.
type Pencari interface {
	// Vektor mencari potongan yang mirip secara makna, tanpa penyaringan.
	Vektor(ctx context.Context, query string) ([]*schema.Document, error)
	// Pasal mengambil seluruh potongan milik pasal-pasal bernomor tertentu.
	Pasal(ctx context.Context, nomor []string) ([]*schema.Document, error)
	// Rujukan mengambil pasal-pasal yang dirujuk silang oleh pasal tertentu,
	// satu lompatan, berikut pasal awalnya.
	Rujukan(ctx context.Context, nomor []string) ([]*schema.Document, error)
}

// pencariAgen menyatukan retriever dasar dan pembungkus per-pasal menjadi satu
// kemampuan yang bisa dipanggil tool.
//
// Karena tinggal di paket yang sama dengan retriever.go, ia boleh memakai
// cariPerPasal, gabung, dan nomorRujukanSilang tanpa mengekspor apa pun dan
// tanpa menyentuh jalur linear.
type pencariAgen struct {
	dasar retriever.Retriever
	pasal *retrieverPasal
}

func newPencariAgen(dasar retriever.Retriever, pasal *retrieverPasal) *pencariAgen {
	return &pencariAgen{dasar: dasar, pasal: pasal}
}

// Vektor memakai retriever DASAR, bukan retrieverPasal.
//
// Ini disengaja: tool vektor harus murni kemiripan makna. Penyaringan per pasal
// yang otomatis di jalur linear justru hal yang ingin diserahkan ke keputusan
// agen lewat tool baca_pasal.
func (p *pencariAgen) Vektor(ctx context.Context, query string) ([]*schema.Document, error) {
	return p.dasar.Retrieve(ctx, query)
}

func (p *pencariAgen) Pasal(ctx context.Context, nomor []string) ([]*schema.Document, error) {
	return p.pasal.cariPerPasal(ctx, queryPasal(nomor), nomor, nil)
}

func (p *pencariAgen) Rujukan(ctx context.Context, nomor []string) ([]*schema.Document, error) {
	awal, err := p.pasal.cariPerPasal(ctx, queryPasal(nomor), nomor, nil)
	if err != nil {
		return nil, err
	}

	rujukan := nomorRujukanSilang(awal, nomor)
	if len(rujukan) == 0 {
		return awal, nil
	}

	lanjutan, err := p.pasal.cariPerPasal(ctx, queryPasal(rujukan), rujukan, nil)
	if err != nil {
		// Rujukan adalah tambahan, bukan syarat — sama seperti ikutiRujukan di
		// jalur linear (retriever.go). Pasal awal tetap dipakai.
		return awal, nil
	}

	return p.pasal.gabung(
		kelompok{awal, asalPasal},
		kelompok{lanjutan, asalRujukan},
	), nil
}

// queryPasal menyusun teks query untuk pencarian per pasal. Nomornya dipakai
// sebagai kata kunci supaya bagian vektor pencarian pun terarah.
func queryPasal(nomor []string) string {
	return "Pasal " + strings.Join(nomor, " ")
}

// --- Bentuk masukan dan keluaran tool ---

// inputCariVektor, inputBacaPasal, dan inputIkutiRujukan adalah skema argumen
// tool yang dibaca model. Deskripsi di tag jsonschema_description ikut dikirim
// ke model, jadi ditulis sebagai petunjuk pemakaian, bukan sekadar keterangan.
type inputCariVektor struct {
	Query string `json:"query" jsonschema_description:"Pertanyaan atau kata kunci pencarian, dalam bahasa Indonesia."`
}

type inputBacaPasal struct {
	Nomor string `json:"nomor" jsonschema_description:"Nomor pasal tanpa kata 'pasal', misalnya \"145\"."`
}

type inputIkutiRujukan struct {
	Nomor string `json:"nomor" jsonschema_description:"Nomor pasal yang isinya ingin ditelusuri rujukan silangnya, misalnya \"145\"."`
}

// potonganHasil adalah satu potongan dokumen beserta label rujukannya, supaya
// model bisa menyebut pasal sumbernya saat menjawab.
type potonganHasil struct {
	Label string `json:"label"`
	Isi   string `json:"isi"`
}

// hasilPencarian adalah keluaran seragam semua tool pencarian.
//
// Catatan diisi ketika tidak ada hasil atau pencarian gagal — sebagai teks,
// bukan error Go. Model perlu tahu bahwa pencarian tidak membuahkan apa-apa
// supaya ia tidak mengarang; tetapi kegagalan satu tool tidak boleh
// menjatuhkan seluruh percakapan.
type hasilPencarian struct {
	Jumlah   int             `json:"jumlah"`
	Potongan []potonganHasil `json:"potongan"`
	Catatan  string          `json:"catatan,omitempty"`
}

// --- Konstruksi tool ---

// susunAlat membuat ketiga tool pencarian di atas Pencari yang diberikan.
func susunAlat(p Pencari) ([]tool.BaseTool, error) {
	pembuat := []func(Pencari) (tool.InvokableTool, error){
		newToolCariVektor,
		newToolBacaPasal,
		newToolIkutiRujukan,
	}

	alat := make([]tool.BaseTool, 0, len(pembuat))
	for _, buat := range pembuat {
		t, err := buat(p)
		if err != nil {
			return nil, err
		}
		alat = append(alat, t)
	}
	return alat, nil
}

func newToolCariVektor(p Pencari) (tool.InvokableTool, error) {
	return utils.InferTool("cari_vektor",
		"Mencari potongan dokumen yang mirip secara makna dengan sebuah query. "+
			"Pakai untuk pertanyaan bertopik yang tidak menyebut nomor pasal.",
		func(ctx context.Context, in inputCariVektor) (hasilPencarian, error) {
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return hasilPencarian{Catatan: "query kosong"}, nil
			}

			docs, err := p.Vektor(ctx, q)
			if err != nil {
				log.Printf("rag: tool cari_vektor gagal: %v", err)
				return hasilPencarian{Catatan: "pencarian vektor gagal: " + err.Error()}, nil
			}

			log.Printf("rag: tool cari_vektor query=%q → %d potongan", q, len(docs))
			catatJejak(ctx, docs)
			return susunHasil(docs), nil
		})
}

func newToolBacaPasal(p Pencari) (tool.InvokableTool, error) {
	return utils.InferTool("baca_pasal",
		"Mengambil SELURUH isi satu pasal berdasarkan nomornya. "+
			"Pakai saat pertanyaan menyebut nomor pasal tertentu.",
		func(ctx context.Context, in inputBacaPasal) (hasilPencarian, error) {
			nomor := angkaPertama(in.Nomor)
			if nomor == "" {
				return hasilPencarian{Catatan: "nomor pasal kosong"}, nil
			}

			docs, err := p.Pasal(ctx, []string{nomor})
			if err != nil {
				log.Printf("rag: tool baca_pasal nomor=%s gagal: %v", nomor, err)
				return hasilPencarian{Catatan: "pencarian pasal gagal: " + err.Error()}, nil
			}

			log.Printf("rag: tool baca_pasal nomor=%s → %d potongan", nomor, len(docs))
			catatJejak(ctx, docs)
			return susunHasil(docs), nil
		})
}

func newToolIkutiRujukan(p Pencari) (tool.InvokableTool, error) {
	return utils.InferTool("ikuti_rujukan",
		"Mengambil pasal-pasal yang dirujuk silang oleh suatu pasal, "+
			"misalnya isi Pasal 145 menyebut \"sebagaimana dimaksud dalam Pasal 25\". "+
			"Pakai setelah baca_pasal bila ada rujukan ke pasal yang belum ada di konteks.",
		func(ctx context.Context, in inputIkutiRujukan) (hasilPencarian, error) {
			nomor := angkaPertama(in.Nomor)
			if nomor == "" {
				return hasilPencarian{Catatan: "nomor pasal kosong"}, nil
			}

			docs, err := p.Rujukan(ctx, []string{nomor})
			if err != nil {
				log.Printf("rag: tool ikuti_rujukan nomor=%s gagal: %v", nomor, err)
				return hasilPencarian{Catatan: "penelusuran rujukan gagal: " + err.Error()}, nil
			}

			// Label ikut dicatat karena di sinilah letak gunanya tool ini:
			// menampilkan pasal mana saja yang akhirnya terambil, termasuk
			// pasal yang dirujuk silang — bukan hanya jumlah potongannya.
			log.Printf("rag: tool ikuti_rujukan nomor=%s → %d potongan %v",
				nomor, len(docs), LabelSumber(docs))
			catatJejak(ctx, docs)
			return susunHasil(docs), nil
		})
}

// susunHasil mengubah potongan dokumen menjadi bentuk yang dikirim balik ke
// model, lengkap dengan label pasalnya.
func susunHasil(docs []*schema.Document) hasilPencarian {
	h := hasilPencarian{Potongan: make([]potonganHasil, 0, len(docs))}
	for _, d := range docs {
		h.Potongan = append(h.Potongan, potonganHasil{Label: labelSumber(d), Isi: d.Content})
	}
	h.Jumlah = len(h.Potongan)
	if h.Jumlah == 0 {
		h.Catatan = "tidak ada potongan yang ditemukan untuk permintaan ini"
	}
	return h
}

// reAngka menangkap deretan angka pertama di dalam teks.
var reAngka = regexp.MustCompile(`\d+`)

// angkaPertama mengambil angka pertama dari argumen nomor yang dikirim model.
//
// Model kadang menulis "Pasal 7" atau "pasal 7 ayat (2)" meskipun deskripsi
// tool sudah meminta nomornya saja. Mengambil deret angka pertama membuat
// ketiganya bekerja tanpa perlu menyalahkan model.
func angkaPertama(s string) string {
	return reAngka.FindString(s)
}

// --- Jejak retrieval per permintaan ---

// jejakRetrieval mengumpulkan semua potongan yang pernah diambil tool selama
// satu permintaan.
//
// Diperlukan karena satu permintaan agen bisa retrieval berkali-kali, sedangkan
// antarmuka — rujukan di bawah jawaban dan saran lanjutan — dibangun dari satu
// kumpulan bukti. Tanpa ini, rujukan hanya akan mencerminkan pencarian terakhir.
//
// Dijaga mutex karena ToolsNode boleh menjalankan beberapa tool bersamaan, dan
// potongan yang sama bisa terambil lewat lebih dari satu tool.
type jejakRetrieval struct {
	mu      sync.Mutex
	docs    []*schema.Document
	dilihat map[string]struct{}
}

func newJejakRetrieval() *jejakRetrieval {
	return &jejakRetrieval{dilihat: make(map[string]struct{})}
}

func (j *jejakRetrieval) catat(docs []*schema.Document) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for _, d := range docs {
		if _, ada := j.dilihat[d.ID]; ada {
			continue
		}
		j.dilihat[d.ID] = struct{}{}
		j.docs = append(j.docs, d)
	}
}

func (j *jejakRetrieval) snapshot() []*schema.Document {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]*schema.Document, len(j.docs))
	copy(out, j.docs)
	return out
}

// sumber menyusun label rujukan dari seluruh potongan yang terkumpul.
func (j *jejakRetrieval) sumber() []string {
	return LabelSumber(j.snapshot())
}

// konteks menggabungkan seluruh potongan yang terkumpul menjadi satu teks,
// dipakai menyusun saran lanjutan.
func (j *jejakRetrieval) konteks() string {
	return formatDocs(j.snapshot())
}

// kunciJejak adalah tipe kunci context untuk membawa jejakRetrieval.
//
// Tipe kosong khusus dipakai supaya kunci ini tidak bentrok dengan nilai
// context lain dari paket mana pun.
type kunciJejak struct{}

func denganJejak(ctx context.Context, j *jejakRetrieval) context.Context {
	return context.WithValue(ctx, kunciJejak{}, j)
}

func jejakDari(ctx context.Context) *jejakRetrieval {
	j, _ := ctx.Value(kunciJejak{}).(*jejakRetrieval)
	return j
}

// catatJejak mencatat potongan ke jejak permintaan, bila ada.
//
// Aman dipanggil tanpa jejak: di luar jalur agen context-nya tidak membawa
// jejak, dan fungsi ini tidak melakukan apa-apa.
func catatJejak(ctx context.Context, docs []*schema.Document) {
	if j := jejakDari(ctx); j != nil {
		j.catat(docs)
	}
}

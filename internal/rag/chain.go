// Package rag menyusun jalur baca (read path) pipeline RAG: mencari potongan
// dokumen yang paling relevan dengan pertanyaan, menggabungkannya menjadi
// konteks, lalu meminta Gemini menyusun jawaban berdasarkan konteks tersebut.
//
// Sebelum retrieval, pertanyaan lanjutan ditulis ulang dulu menjadi pertanyaan
// mandiri dengan bantuan riwayat percakapan (lihat condenseQuestion), sehingga
// rujukan seperti "poin a di atas" tetap bisa dicari di vector store.
//
// Ini adalah padanan sel-sel notebook mulai dari "Membuat Retriever" sampai
// "RAG Chain".
package rag

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	qdrantretriever "github.com/cloudwego/eino-ext/components/retriever/qdrant"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"

	"rag-eino/internal/config"
	"rag-eino/internal/gemini"
)

// systemInstruction adalah instruksi tetap yang menentukan peran model.
// Padanan SystemMessage di notebook.
//
// Kalimat terakhir soal riwayat ditambahkan karena percakapan bisa berjalan
// beberapa giliran: tanpa itu model tidak tahu bahwa pesan-pesan sebelumnya
// ikut disertakan, dan rujukan seperti "poin a di atas" dianggap pertanyaan
// lepas.
const systemInstruction = `Anda adalah AI yang dapat menjawab pertanyaan berdasarkan konteks dan pertanyaan dari user.
Anda harus menjawab pertanyaan user, berdasarkan konteks.
Bila ada riwayat percakapan sebelumnya, pakai riwayat itu untuk memahami
rujukan seperti "poin a di atas" atau "yang tadi". Jawaban tetap harus
bersumber dari konteks dokumen, bukan dari riwayat.`

// condenseInstruction menugaskan model untuk menulis ulang pertanyaan lanjutan
// menjadi pertanyaan mandiri. Ini padanan prompt di
// create_history_aware_retriever pada LangChain.
//
// Tanpa langkah ini, pertanyaan seperti "naskah poin a di atas itu apa?" tidak
// punya kata kunci yang bisa dicocokkan ke vector store, sehingga retrieval
// mengambil chunk yang salah walaupun riwayatnya lengkap.
const condenseInstruction = `Diberikan riwayat percakapan dan pertanyaan lanjutan pengguna,
tulis ulang pertanyaan lanjutan itu menjadi pertanyaan mandiri yang utuh dan
bisa dipahami tanpa riwayat. Jangan menjawab pertanyaan. Jangan menambah
informasi baru. Jika pertanyaan sudah mandiri, kembalikan apa adanya.
Balas HANYA pertanyaan hasil tulis ulang.`

// userTemplate adalah template pesan dari user. Placeholder {context} dan
// {question} diisi otomatis saat chain dijalankan. Padanan
// HumanMessagePromptTemplate di notebook.
const userTemplate = `Jawab pertanyaan berikut berdasarkan konteks.
konteks: {context}
pertanyaan: {question}
jawaban: `

// Pertanyaan adalah masukan satu giliran percakapan: teks pertanyaan ditambah
// riwayat pesan sebelumnya. Riwayat kosong berarti percakapan baru.
type Pertanyaan struct {
	Teks    string
	Riwayat []*schema.Message
	// OnSumber dipanggil sekali setelah retrieval selesai, berisi label rujukan
	// potongan yang dipakai menyusun konteks — mis. "Pasal 7". Boleh nil.
	//
	// Ditaruh di sini, bukan sebagai parameter Ask/Stream, karena hasil
	// retrieval baru diketahui di tengah graph — node prepare yang
	// menjalankannya. Callback yang dipegang pemanggil adalah satu-satunya cara
	// mengabarkan hasil itu tanpa menahan aliran jawaban sampai graph selesai.
	OnSumber func([]string)
}

// langkah adalah nilai yang mengalir dari node condense ke node prepare.
//
// Query dan Asli sengaja dipisah: retrieval harus memakai pertanyaan yang
// sudah ditulis ulang supaya kata kuncinya cocok dengan isi dokumen,
// sementara model tetap menerima pertanyaan asli pengguna apa adanya.
type langkah struct {
	Query    string
	Asli     string
	Riwayat  []*schema.Message
	OnSumber func([]string)
}

// Chain adalah pipeline RAG yang sudah dirakit dan siap dipanggil.
type Chain struct {
	run compose.Runnable[Pertanyaan, string]
	// ret disimpan supaya retrieval bisa dijalankan sendiri lewat Cari, tanpa
	// condense dan tanpa model.
	ret retriever.Retriever
}

// New merakit seluruh komponen RAG menjadi satu chain.
//
// Susunan node-nya:
//
//	START -> condense -> prepare -> prompt -> model -> parse -> END
//
// Ini padanan rangkaian pipe LCEL di notebook, tapi ditulis sebagai graph
// sehingga setiap tahap bisa dilihat dan diganti satu per satu.
func New(ctx context.Context, cfg *config.Config, ai *gemini.Client, qc *qdrantclient.Client) (*Chain, error) {
	ret, err := qdrantretriever.NewRetriever(ctx, &qdrantretriever.Config{
		Client:     qc,
		Collection: cfg.QdrantCollection,
		// QueryEmbedder (TaskType RETRIEVAL_QUERY) harus dipakai di sini,
		// bukan DocEmbedder, supaya vektor pertanyaan sebanding dengan
		// vektor dokumen yang sudah tersimpan.
		Embedding: ai.QueryEmbedder,
		TopK:      cfg.TopK,
	})
	if err != nil {
		return nil, fmt.Errorf("membuat retriever Qdrant: %w", err)
	}

	// Dibungkus supaya pertanyaan yang menyebut nomor pasal — "pasal 1
	// membahas apa" — mengambil seluruh isi pasal itu, bukan hanya potongan
	// yang kebetulan mirip secara vektor. Lihat retriever.go.
	return newDenganRetriever(ctx, ai, newRetrieverPasal(ret))
}

// newDenganRetriever merakit chain dari retriever yang diberikan.
//
// Dipisah dari New supaya pengujian bisa menyuntikkan retriever palsu. Tanpa
// seam ini, menjalankan chain di dalam test akan memanggil Qdrant sungguhan,
// padahal yang perlu diuji adalah perilaku aliran antar-node.
func newDenganRetriever(ctx context.Context, ai *gemini.Client, ret retriever.Retriever) (*Chain, error) {
	// MessagesPlaceholder menandai posisi pesan-pesan riwayat di antara
	// instruksi sistem dan pesan user. Diberi optional=true supaya giliran
	// pertama (tanpa riwayat) tetap bisa dijalankan tanpa kunci "history".
	chatTemplate := prompt.FromMessages(schema.FString,
		schema.SystemMessage(systemInstruction),
		schema.MessagesPlaceholder("history", true),
		schema.UserMessage(userTemplate),
	)

	graph := compose.NewGraph[Pertanyaan, string]()

	if err := graph.AddLambdaNode("condense", compose.InvokableLambda(condenseQuestion(ai.ChatModel))); err != nil {
		return nil, fmt.Errorf("menambah node condense: %w", err)
	}
	if err := graph.AddLambdaNode("prepare", compose.InvokableLambda(prepareContext(ret))); err != nil {
		return nil, fmt.Errorf("menambah node prepare: %w", err)
	}
	if err := graph.AddChatTemplateNode("prompt", chatTemplate); err != nil {
		return nil, fmt.Errorf("menambah node prompt: %w", err)
	}
	if err := graph.AddChatModelNode("model", ai.ChatModel); err != nil {
		return nil, fmt.Errorf("menambah node model: %w", err)
	}
	if err := graph.AddLambdaNode("parse", compose.TransformableLambda(parseStream)); err != nil {
		return nil, fmt.Errorf("menambah node parse: %w", err)
	}

	edges := [][2]string{
		{compose.START, "condense"},
		{"condense", "prepare"},
		{"prepare", "prompt"},
		{"prompt", "model"},
		{"model", "parse"},
		{"parse", compose.END},
	}
	for _, e := range edges {
		if err := graph.AddEdge(e[0], e[1]); err != nil {
			return nil, fmt.Errorf("menghubungkan %s -> %s: %w", e[0], e[1], err)
		}
	}

	run, err := graph.Compile(ctx)
	if err != nil {
		return nil, fmt.Errorf("menyusun chain RAG: %w", err)
	}

	return &Chain{run: run, ret: ret}, nil
}

// Ask menjalankan chain untuk satu giliran percakapan dan mengembalikan
// jawaban model.
func (c *Chain) Ask(ctx context.Context, p Pertanyaan) (string, error) {
	return c.run.Invoke(ctx, p)
}

// Stream menjalankan chain untuk satu giliran percakapan dan mengembalikan
// jawaban sebagai aliran potongan teks, supaya pemanggil bisa menampilkannya
// bertahap alih-alih menunggu jawaban lengkap.
//
// Pemanggil wajib memanggil Close pada reader yang dikembalikan.
func (c *Chain) Stream(ctx context.Context, p Pertanyaan) (*schema.StreamReader[string], error) {
	return c.run.Stream(ctx, p)
}

// PangkasRiwayat membatasi riwayat ke maks pesan terakhir. Dipotong dari depan
// supaya giliran terbaru — yang paling menentukan rujukan seperti "poin a di
// atas" — selalu utuh.
//
// maks 0 atau negatif berarti memori dimatikan: hasilnya nil, bukan riwayat
// penuh. Ini disengaja supaya HISTORY_MESSAGES=0 punya satu arti yang sama di
// CLI maupun web, alih-alih diam-diam berarti "tanpa batas".
func PangkasRiwayat(riwayat []*schema.Message, maks int) []*schema.Message {
	if maks <= 0 {
		return nil
	}
	if len(riwayat) <= maks {
		return riwayat
	}
	return riwayat[len(riwayat)-maks:]
}

// condenseQuestion adalah padanan create_history_aware_retriever di LangChain:
// sebelum retrieval, pertanyaan lanjutan ditulis ulang menjadi pertanyaan
// mandiri dengan bantuan riwayat.
//
// Node ini sengaja InvokableLambda, bukan TransformableLambda. Panggilan
// modelnya memang non-streaming (Generate), dan hasilnya harus utuh sebelum
// dipakai sebagai query retrieval — tidak ada aliran yang perlu dijaga di
// sini. Node parse yang tetap TransformableLambda adalah yang menyentuh
// jawaban akhir.
//
// Kalau riwayat kosong, model tidak dipanggil sama sekali. Selain menghemat
// kuota, ini juga yang membuat giliran pertama berperilaku persis seperti
// sebelum fitur memori ada.
func condenseQuestion(cm model.BaseChatModel) func(context.Context, Pertanyaan) (langkah, error) {
	return func(ctx context.Context, in Pertanyaan) (langkah, error) {
		langsung := langkah{Query: in.Teks, Asli: in.Teks, Riwayat: in.Riwayat, OnSumber: in.OnSumber}
		if len(in.Riwayat) == 0 {
			return langsung, nil
		}

		msgs := make([]*schema.Message, 0, len(in.Riwayat)+2)
		msgs = append(msgs, schema.SystemMessage(condenseInstruction))
		msgs = append(msgs, in.Riwayat...)
		msgs = append(msgs, schema.UserMessage(in.Teks))

		out, err := cm.Generate(ctx, msgs)

		// Dua-duanya jatuh ke pertanyaan asli: error (kuota habis, jaringan
		// putus) dan hasil kosong. Hasil kosong tetap berbahaya walau tidak
		// error — query kosong akan mengembalikan chunk acak dari Qdrant,
		// yang lebih buruk daripada memakai pertanyaan asli.
		if err != nil {
			log.Printf("rag: menulis ulang pertanyaan gagal, memakai pertanyaan asli: %v", err)
			return langsung, nil
		}
		if out == nil || strings.TrimSpace(out.Content) == "" {
			log.Printf("rag: menulis ulang pertanyaan menghasilkan teks kosong, memakai pertanyaan asli")
			return langsung, nil
		}

		query := strings.TrimSpace(out.Content)
		// Dicatat karena tanpa ini, pertanyaan yang dipakai retrieval tidak
		// terlihat sama sekali — padahal di situlah letak bedanya jawaban
		// yang tepat dan yang melenceng.
		log.Printf("pertanyaan ditulis ulang: %q", query)

		return langkah{
			Query:    query,
			Asli:     in.Teks,
			Riwayat:  in.Riwayat,
			OnSumber: in.OnSumber,
		}, nil
	}
}

// prepareContext adalah padanan baris LCEL di notebook:
//
//	{"context": retriever | format_docs, "question": RunnablePassthrough()}
//
// Node ini mengambil potongan dokumen yang paling relevan dengan query hasil
// condense, menggabungkannya menjadi satu teks konteks, lalu mengembalikan
// semuanya sebagai map supaya bisa diisi ke placeholder di prompt template.
func prepareContext(ret retriever.Retriever) func(context.Context, langkah) (map[string]any, error) {
	return func(ctx context.Context, in langkah) (map[string]any, error) {
		docs, err := ret.Retrieve(ctx, in.Query)
		if err != nil {
			return nil, fmt.Errorf("mencari dokumen relevan: %w", err)
		}

		log.Printf("retriever mengembalikan %d chunk", len(docs))

		// Dikabarkan sebelum model mulai menjawab, supaya antarmuka bisa
		// menampilkan rujukannya lebih dulu alih-alih menunggu jawaban utuh.
		if in.OnSumber != nil {
			in.OnSumber(LabelSumber(docs))
		}

		out := map[string]any{
			"context": formatDocs(docs),
			// Pertanyaan asli, bukan hasil condense: pengguna harus melihat
			// jawaban atas apa yang benar-benar ia tanyakan.
			"question": in.Asli,
		}

		// Kunci "history" hanya dipasang kalau ada isinya.
		//
		// MessagesPlaceholder melakukan type assertion ke []*schema.Message,
		// jadi nilainya harus bertipe itu — nilai bertipe lain, termasuk nil
		// tanpa tipe, ditolak dengan "only messages can be used to format
		// message placeholder". Flag optional=true tidak menolong di sini:
		// ia hanya berlaku ketika kuncinya tidak ada sama sekali.
		if len(in.Riwayat) > 0 {
			out["history"] = in.Riwayat
		}

		return out, nil
	}
}

// formatDocs menggabungkan semua chunk yang dikembalikan retriever menjadi
// satu string, dipisahkan dua baris kosong. Padanan fungsi format_docs di
// notebook.
func formatDocs(docs []*schema.Document) string {
	parts := make([]string, 0, len(docs))
	for _, d := range docs {
		parts = append(parts, d.Content)
	}
	return strings.Join(parts, "\n\n")
}

// parseStream mengubah aliran pesan dari model menjadi aliran teks.
// Padanan StrOutputParser di notebook.
//
// Node ini wajib memakai TransformableLambda, bukan InvokableLambda. Eino
// menurunkan fungsi transform untuk InvokableLambda lewat transformByInvoke,
// yang lebih dulu mengumpulkan (Collect) seluruh stream menjadi satu nilai
// sebelum memanggil lambda sekali. Kalau itu terjadi, jawaban yang seharusnya
// mengalir bertahap akan tertahan sampai model selesai — streaming token mati
// tepat di node ini. TransformableLambda memetakan setiap chunk apa adanya,
// sehingga aliran tetap hidup dari model sampai ke pemanggil.
//
// Mode Invoke (method Ask) tetap bekerja tanpa perubahan: Eino menyediakan
// fallback invokeByTransform yang membungkus satu nilai menjadi stream,
// menjalankan transform, lalu mengumpulkan hasilnya kembali.
func parseStream(_ context.Context, sr *schema.StreamReader[*schema.Message]) (*schema.StreamReader[string], error) {
	return schema.StreamReaderWithConvert(sr, func(msg *schema.Message) (string, error) {
		if msg == nil {
			return "", errors.New("model tidak mengembalikan jawaban")
		}
		return msg.Content, nil
	}), nil
}

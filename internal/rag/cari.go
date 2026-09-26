package rag

import (
	"context"
	"fmt"
	"strconv"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	qdrantclient "github.com/qdrant/go-client/qdrant"
)

// kunciPayloadMetadata adalah kunci tempat indexer Qdrant menyimpan
// doc.MetaData.
//
// Metadata tidak disimpan sebagai kunci tingkat atas payload, melainkan
// dikumpulkan di bawah satu kunci bernama "metadata". Jadi pasal sebuah chunk
// beralamat "metadata.pasal", bukan "pasal" — perbedaan yang menentukan saat
// menyusun filter Qdrant nanti.
const kunciPayloadMetadata = "metadata"

// Temuan adalah satu hasil pencarian retrieval, lengkap dengan metadata
// potongannya.
type Temuan struct {
	// Peringkat dimulai dari 1, mengikuti urutan skor menurun dari Qdrant.
	Peringkat int
	Skor      float64
	// Asal menunjukkan mengapa potongan ini ikut terambil: "pasal" kalau ia
	// diambil karena nomor pasal yang disebut di pertanyaan, "vektor" kalau
	// karena kemiripan makna. Kosong kalau retriever-nya tidak membedakan.
	Asal string
	// Meta berisi metadata potongan apa adanya. Isinya bergantung pada
	// strategi pemotongan yang dipakai saat ingest.
	Meta map[string]string
	Isi  string
}

// Cari menjalankan retrieval saja: tanpa penulisan ulang pertanyaan dan tanpa
// model.
//
// Ini alat diagnosis, bukan jalan pintas menjawab. Bedanya penting: jawaban
// akhir yang salah bisa berasal dari potongan yang tidak terambil, atau dari
// potongan yang terambil tapi disimpulkan keliru oleh model. Dari jawaban
// akhir keduanya terlihat sama; dari sini tidak.
//
// topK 0 atau negatif berarti memakai TOP_K dari konfigurasi.
func (c *Chain) Cari(ctx context.Context, query string, topK int) ([]Temuan, error) {
	var opts []retriever.Option
	if topK > 0 {
		opts = append(opts, retriever.WithTopK(topK))
	}

	docs, err := c.ret.Retrieve(ctx, query, opts...)
	if err != nil {
		return nil, fmt.Errorf("mencari potongan relevan: %w", err)
	}

	hasil := make([]Temuan, 0, len(docs))
	for i, d := range docs {
		hasil = append(hasil, Temuan{
			Peringkat: i + 1,
			Skor:      d.Score(),
			Asal:      bacaAsal(d),
			Meta:      bacaMetadata(d),
			Isi:       d.Content,
		})
	}

	return hasil, nil
}

// bacaAsal mengambil penanda asal potongan, kalau retriever-nya mengisinya.
func bacaAsal(doc *schema.Document) string {
	asal, _ := doc.MetaData[kunciAsal].(string)
	return asal
}

// bacaMetadata menyalin metadata potongan dari bentuk payload Qdrant ke map
// string biasa.
//
// Tipenya adalah map[string]*qdrant.Value, BUKAN map[string]*structpb.Value.
// Client Qdrant memakai tipe Value buatan sendiri: ia mengikuti structpb, tapi
// memisahkan IntegerValue dan DoubleValue dari satu NumberValue. Karena itu ia
// tidak punya AsInterface seperti structpb, dan konversinya ditulis di sini.
//
// Mudah tertukar, dan salah menebak di sini gagalnya diam-diam: type assertion
// gagal, map-nya nil, dan seluruh metadata hilang dari keluaran tanpa satu pun
// error.
func bacaMetadata(doc *schema.Document) map[string]string {
	mentah, ok := doc.MetaData[kunciPayloadMetadata].(map[string]*qdrantclient.Value)
	if !ok {
		return nil
	}

	out := make(map[string]string, len(mentah))
	for k, v := range mentah {
		out[k] = nilaiKeString(v)
	}
	return out
}

// nilaiKeString mengubah satu nilai payload Qdrant menjadi teks.
//
// Nilainya dikonversi menurut tipenya, bukan lewat GetStringValue. Alasannya:
// GetStringValue mengembalikan "" untuk nilai yang bukan string, sehingga
// metadata bertipe lain akan tampil seolah-olah kosong. Untuk perintah yang
// tugasnya menunjukkan apa yang benar-benar tersimpan, menyembunyikan tipe
// yang tak terduga justru menghapus gunanya.
func nilaiKeString(v *qdrantclient.Value) string {
	switch k := v.GetKind().(type) {
	case *qdrantclient.Value_NullValue:
		return ""
	case *qdrantclient.Value_StringValue:
		return k.StringValue
	case *qdrantclient.Value_IntegerValue:
		return strconv.FormatInt(k.IntegerValue, 10)
	case *qdrantclient.Value_DoubleValue:
		return strconv.FormatFloat(k.DoubleValue, 'g', -1, 64)
	case *qdrantclient.Value_BoolValue:
		return strconv.FormatBool(k.BoolValue)
	default:
		// Struct dan list ditampilkan apa adanya lewat bentuk teks protobuf.
		// Metadata chunk selalu datar, jadi cabang ini hanya jaring pengaman
		// supaya nilai bertingkat tidak menghilang dari keluaran.
		return v.String()
	}
}

package ingest

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// TestAssignChunkIDsDeterministik memastikan ID chunk hanya bergantung pada isi
// chunk, bukan pada urutan pemanggilan atau keacakan.
//
// Sifat inilah yang membuat ingest idempotent: menjalankan ingest dua kali
// untuk dokumen yang sama menghasilkan ID yang sama, sehingga Qdrant menimpa
// titik lama alih-alih menggandakannya.
func TestAssignChunkIDsDeterministik(t *testing.T) {
	isi := []string{"Kapan bayi mulai diberi MPASI?", "Bayi mulai diberi MPASI pada usia 6 bulan."}

	pertama := dokumenDari(isi)
	kedua := dokumenDari(isi)

	assignChunkIDs(pertama)
	assignChunkIDs(kedua)

	for i := range isi {
		if pertama[i].ID != kedua[i].ID {
			t.Errorf("chunk %d: ID berubah antar pemanggilan (%q vs %q)",
				i, pertama[i].ID, kedua[i].ID)
		}
	}

	// Isi berbeda harus menghasilkan ID berbeda.
	if pertama[0].ID == pertama[1].ID {
		t.Error("chunk dengan isi berbeda mendapat ID yang sama")
	}
}

// TestAssignChunkIDsSelaluUUIDValid memastikan setiap ID bisa dipakai sebagai
// point ID di Qdrant, yang hanya menerima UUID atau bilangan bulat.
func TestAssignChunkIDsSelaluUUIDValid(t *testing.T) {
	chunks := dokumenDari([]string{"a", "b", ""})
	assignChunkIDs(chunks)

	for i, c := range chunks {
		if c.ID == "" {
			t.Errorf("chunk %d: ID kosong", i)
			continue
		}
		if _, err := uuid.Parse(c.ID); err != nil {
			t.Errorf("chunk %d: ID %q bukan UUID yang valid: %v", i, c.ID, err)
		}
	}
}

// TestAssignChunkIDsIsiKembarSatuTitik mendokumentasikan konsekuensi yang
// disengaja: teks yang benar-benar kembar dianggap satu titik.
func TestAssignChunkIDsIsiKembarSatuTitik(t *testing.T) {
	chunks := dokumenDari([]string{"teks kembar", "teks kembar"})
	assignChunkIDs(chunks)

	if chunks[0].ID != chunks[1].ID {
		t.Errorf("chunk dengan isi identik seharusnya mendapat ID yang sama, dapat %q dan %q",
			chunks[0].ID, chunks[1].ID)
	}
}

func dokumenDari(isi []string) []*schema.Document {
	docs := make([]*schema.Document, 0, len(isi))
	for _, s := range isi {
		docs = append(docs, &schema.Document{Content: s})
	}
	return docs
}

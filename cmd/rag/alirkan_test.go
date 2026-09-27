package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"rag-eino/internal/rag"
)

// fakePenjawab adalah rag.Penjawab palsu untuk menguji adapter aliran, tanpa
// menyentuh Qdrant maupun Gemini.
type fakePenjawab struct {
	potongan []string
	konteks  string
	sumber   []string

	// blokir membuat Stream menunggu context dibatalkan sebelum kembali, untuk
	// menguji jaring pengaman waktu.
	blokir bool
	err    error
}

func (f *fakePenjawab) Ask(context.Context, rag.Pertanyaan) (string, error) {
	return "", errors.New("Ask tidak dipakai di test ini")
}

func (f *fakePenjawab) Stream(ctx context.Context, p rag.Pertanyaan) (*schema.StreamReader[string], error) {
	if f.blokir {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}

	// Menyalakan callback sebelum aliran dibaca, meniru jalur baca yang
	// mengabarkan hasil retrieval lebih dulu.
	if p.OnKonteks != nil {
		p.OnKonteks(f.konteks)
	}
	if p.OnSumber != nil {
		p.OnSumber(f.sumber)
	}

	return schema.StreamReaderFromArray(f.potongan), nil
}

// TestAlirkanDariPenjawabMeneruskanDelta memastikan setiap potongan jawaban
// diteruskan ke delta, dan rujukan sampai ke callback sumber.
func TestAlirkanDariPenjawabMeneruskanDelta(t *testing.T) {
	pj := &fakePenjawab{
		potongan: []string{"Pasal 7 ", "mengatur surat kuasa."},
		sumber:   []string{"Pasal 7"},
	}

	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 0)

	var (
		delta  []string
		sumber []string
	)
	saluran, err := alirkan(context.Background(), "pasal 7 mengatur apa?", nil,
		func(potongan string) error { delta = append(delta, potongan); return nil },
		func(label []string) error { sumber = label; return nil },
	)
	if err != nil {
		t.Fatalf("alirkan: %v", err)
	}

	if len(delta) != 2 || delta[0] != "Pasal 7 " || delta[1] != "mengatur surat kuasa." {
		t.Errorf("delta = %v, mau [Pasal 7  mengatur surat kuasa.]", delta)
	}
	if len(sumber) != 1 || sumber[0] != "Pasal 7" {
		t.Errorf("sumber = %v, mau [Pasal 7]", sumber)
	}

	// Saran dimatikan (jumlahSaran=0), jadi salurannya harus sudah tertutup.
	if _, ok := <-saluran; ok {
		t.Error("saluran saran masih terbuka, mau tertutup karena SARAN_COUNT=0")
	}
}

// TestAlirkanDariPenjawabMenyaringRujukan mengunci perbaikan yang memicu fitur
// ini: daftar dari jalur baca memuat semua potongan yang pernah terambil —
// di jalur agen bisa puluhan — tetapi yang sampai ke pemanggil hanya pasal
// yang benar-benar disebut jawaban.
func TestAlirkanDariPenjawabMenyaringRujukan(t *testing.T) {
	pj := &fakePenjawab{
		potongan: []string{"Berdasarkan Pasal 8, ada tiga jenis."},
		sumber:   []string{"Pasal 7", "Pasal 8", "Pasal 145", "Pasal 94"},
	}

	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 0)

	var sumber []string
	if _, err := alirkan(context.Background(), "apa saja naskah dinas arahan?", nil,
		func(string) error { return nil },
		func(label []string) error { sumber = label; return nil },
	); err != nil {
		t.Fatalf("alirkan: %v", err)
	}

	mau := []string{"Pasal 8"}
	if !reflect.DeepEqual(sumber, mau) {
		t.Errorf("sumber = %v, mau %v", sumber, mau)
	}
}

// TestAlirkanDariPenjawabTanpaSitasiTidakMengirimRujukan memastikan daftar
// yang tidak dikutip jawaban tidak sampai ke pemanggil. Kalau tetap dikirim,
// blok "Rujukan:" tampil tanpa satu pun label di belakangnya.
func TestAlirkanDariPenjawabTanpaSitasiTidakMengirimRujukan(t *testing.T) {
	pj := &fakePenjawab{
		potongan: []string{"Jawaban yang tidak menyebut pasal mana pun."},
		sumber:   []string{"Pasal 7", "Pasal 8"},
	}

	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 0)

	dipanggil := false
	if _, err := alirkan(context.Background(), "tanya", nil,
		func(string) error { return nil },
		func([]string) error { dipanggil = true; return nil },
	); err != nil {
		t.Fatalf("alirkan: %v", err)
	}

	if dipanggil {
		t.Error("sumber dipanggil padahal jawaban tidak menyebut satu pun pasal")
	}
}

// TestAlirkanDariPenjawabRujukanSetelahDelta mengunci urutan: rujukan baru
// dikirim setelah potongan terakhir, karena penyaringannya butuh jawaban utuh.
// Sebelumnya ia mendahului delta pertama.
func TestAlirkanDariPenjawabRujukanSetelahDelta(t *testing.T) {
	pj := &fakePenjawab{
		potongan: []string{"Berdasarkan ", "Pasal 8, ada tiga jenis."},
		sumber:   []string{"Pasal 8"},
	}

	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 0)

	var urutan []string
	if _, err := alirkan(context.Background(), "apa saja naskah dinas arahan?", nil,
		func(string) error { urutan = append(urutan, "delta"); return nil },
		func([]string) error { urutan = append(urutan, "sumber"); return nil },
	); err != nil {
		t.Fatalf("alirkan: %v", err)
	}

	mau := []string{"delta", "delta", "sumber"}
	if !reflect.DeepEqual(urutan, mau) {
		t.Errorf("urutan = %v, mau %v", urutan, mau)
	}
}

// TestAlirkanDariPenjawabMeneruskanErrorDelta memastikan kegagalan mengirim
// potongan menghentikan aliran dan dilaporkan ke pemanggil.
func TestAlirkanDariPenjawabMeneruskanErrorDelta(t *testing.T) {
	pj := &fakePenjawab{potongan: []string{"satu"}}
	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 0)

	mau := errors.New("klien memutus koneksi")
	_, err := alirkan(context.Background(), "tanya", nil,
		func(string) error { return mau },
		func([]string) error { return nil },
	)
	if !errors.Is(err, mau) {
		t.Fatalf("err = %v, mau %v", err, mau)
	}
}

// TestAlirkanDariPenjawabBatasWaktu memastikan jaring pengaman waktu memutus
// jalur baca yang menggantung.
//
// Tanpa batas ini, satu permintaan agen yang macet bisa menahan koneksi tanpa
// batas — tepat yang hendak dicegah AGENT_TIMEOUT_SECONDS.
func TestAlirkanDariPenjawabBatasWaktu(t *testing.T) {
	pj := &fakePenjawab{blokir: true}
	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 50*time.Millisecond)

	mulai := time.Now()
	_, err := alirkan(context.Background(), "tanya", nil,
		func(string) error { return nil },
		func([]string) error { return nil },
	)
	if err == nil {
		t.Fatal("alirkan berhasil, mau error karena batas waktu")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, mau context.DeadlineExceeded", err)
	}
	if lewat := time.Since(mulai); lewat < 50*time.Millisecond {
		t.Errorf("kembali setelah %v, mau menunggu minimal 50ms", lewat)
	}
}

// TestAlirkanDariPenjawabTanpaBatasTidakMembungkus memastikan batas 0 tidak
// memasang timeout — jalur linear biasa tetap tanpa batas tambahan.
func TestAlirkanDariPenjawabTanpaBatasTidakMembungkus(t *testing.T) {
	pj := &fakePenjawab{potongan: []string{"jawaban"}}
	alirkan := alirkanDariPenjawab(pj, nil, 6, 0, 0)

	var delta []string
	if _, err := alirkan(context.Background(), "tanya", nil,
		func(p string) error { delta = append(delta, p); return nil },
		func([]string) error { return nil },
	); err != nil {
		t.Fatalf("alirkan: %v", err)
	}
	if len(delta) != 1 || delta[0] != "jawaban" {
		t.Errorf("delta = %v, mau [jawaban]", delta)
	}
}

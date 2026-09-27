package rag

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// fakePenjawab adalah Penjawab palsu untuk menguji penjawabCadangan.
//
// Callbacknya sengaja bisa dinyalakan "saat Stream dipanggil" supaya bisa
// membedakan callback jalur utama dari callback jalur cadangan saat test
// memeriksa dari mana rujukan berasal.
type fakePenjawab struct {
	jawaban string
	askErr  error

	potongan []string
	// streamErr membuat Stream gagal total (tidak mengembalikan aliran).
	streamErr error
	// errAlir, bila diisi, dikirim sebagai error SETELAH potongan habis. Bila
	// potongan kosong, artinya gagal sebelum potongan pertama.
	errAlir error

	konteks string
	sumber  []string
	// callbackSaatStream menyalakan callback begitu Stream dipanggil, meniru
	// jalur linear yang mengabarkan hasil retrieval sebelum model menulis.
	callbackSaatStream bool

	jumlahAsk    int
	jumlahStream int
}

func (f *fakePenjawab) Ask(_ context.Context, _ Pertanyaan) (string, error) {
	f.jumlahAsk++
	if f.askErr != nil {
		return "", f.askErr
	}
	return f.jawaban, nil
}

func (f *fakePenjawab) Stream(_ context.Context, p Pertanyaan) (*schema.StreamReader[string], error) {
	f.jumlahStream++

	if f.callbackSaatStream {
		if p.OnKonteks != nil {
			p.OnKonteks(f.konteks)
		}
		if p.OnSumber != nil {
			p.OnSumber(f.sumber)
		}
	}

	if f.streamErr != nil {
		return nil, f.streamErr
	}
	if f.errAlir != nil {
		return streamLaluGalat(f.potongan, f.errAlir), nil
	}

	potongan := make([]string, len(f.potongan))
	copy(potongan, f.potongan)
	return schema.StreamReaderFromArray(potongan), nil
}

// streamLaluGalat mengembalikan aliran yang mengeluarkan potongan-potongan,
// lalu error di ujungnya.
func streamLaluGalat(potongan []string, err error) *schema.StreamReader[string] {
	sr, sw := schema.Pipe[string](1)

	go func() {
		defer sw.Close()
		for _, p := range potongan {
			if sw.Send(p, nil) {
				return
			}
		}
		sw.Send("", err)
	}()

	return sr
}

// TestCadanganAskPakaiUtamaSaatBerhasil memastikan jalur utama dipakai apa
// adanya ketika ia berhasil.
func TestCadanganAskPakaiUtamaSaatBerhasil(t *testing.T) {
	utama := &fakePenjawab{jawaban: "dari agen"}
	cadangan := &fakePenjawab{jawaban: "dari linear"}
	pj := &penjawabCadangan{utama: utama, cadangan: cadangan}

	jawaban, err := pj.Ask(context.Background(), Pertanyaan{Teks: "tanya"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if jawaban != "dari agen" {
		t.Errorf("jawaban = %q, mau %q", jawaban, "dari agen")
	}
	if cadangan.jumlahAsk != 0 {
		t.Errorf("cadangan dipanggil %d kali, mau 0", cadangan.jumlahAsk)
	}
}

// TestCadanganAskJatuhSaatKosong memastikan jawaban kosong dari agen dianggap
// kegagalan, bukan jawaban sah.
//
// Agen yang mengembalikan teks kosong tidak berguna bagi pengguna; lebih baik
// dijawab ulang lewat jalur linear daripada menampilkan halaman kosong.
func TestCadanganAskJatuhSaatKosong(t *testing.T) {
	utama := &fakePenjawab{jawaban: "   "}
	cadangan := &fakePenjawab{jawaban: "dari linear"}
	pj := &penjawabCadangan{utama: utama, cadangan: cadangan}

	jawaban, err := pj.Ask(context.Background(), Pertanyaan{Teks: "tanya"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if jawaban != "dari linear" {
		t.Errorf("jawaban = %q, mau %q", jawaban, "dari linear")
	}
	if cadangan.jumlahAsk != 1 {
		t.Errorf("cadangan dipanggil %d kali, mau 1", cadangan.jumlahAsk)
	}
}

// TestCadanganPakaiUtamaSaatBerhasil memastikan aliran agen diteruskan apa
// adanya, callback-nya menyala sekali dengan urutan konteks lalu sumber, dan
// jalur cadangan tidak tersentuh.
func TestCadanganPakaiUtamaSaatBerhasil(t *testing.T) {
	utama := &fakePenjawab{
		potongan:           []string{"satu ", "dua"},
		konteks:            "konteks agen",
		sumber:             []string{"Pasal 7"},
		callbackSaatStream: true,
	}
	cadangan := &fakePenjawab{potongan: []string{"cadangan"}}
	pj := &penjawabCadangan{utama: utama, cadangan: cadangan}

	var (
		urutan  []string
		konteks string
		sumber  []string
	)
	sr, err := pj.Stream(context.Background(), Pertanyaan{
		Teks:      "tanya",
		OnKonteks: func(k string) { urutan = append(urutan, "konteks"); konteks = k },
		OnSumber:  func(s []string) { urutan = append(urutan, "sumber"); sumber = s },
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	if got := kurasStream(t, sr); len(got) != 2 || got[0] != "satu " || got[1] != "dua" {
		t.Errorf("potongan = %v, mau [satu  dua]", got)
	}
	if len(urutan) != 2 || urutan[0] != "konteks" || urutan[1] != "sumber" {
		t.Errorf("urutan callback = %v, mau [konteks sumber]", urutan)
	}
	if konteks != "konteks agen" {
		t.Errorf("konteks = %q, mau %q", konteks, "konteks agen")
	}
	if len(sumber) != 1 || sumber[0] != "Pasal 7" {
		t.Errorf("sumber = %v, mau [Pasal 7]", sumber)
	}
	if cadangan.jumlahStream != 0 {
		t.Errorf("cadangan dipanggil %d kali, mau 0", cadangan.jumlahStream)
	}
}

// TestCadanganJatuhKeLinearSaatGagalMulai memastikan kegagalan agen sebelum
// aliran terbentuk dialihkan ke jalur linear.
func TestCadanganJatuhKeLinearSaatGagalMulai(t *testing.T) {
	utama := &fakePenjawab{streamErr: errors.New("agen gagal mulai")}
	cadangan := &fakePenjawab{
		potongan:           []string{"dari linear"},
		konteks:            "konteks linear",
		sumber:             []string{"Pasal 9"},
		callbackSaatStream: true,
	}
	pj := &penjawabCadangan{utama: utama, cadangan: cadangan}

	var sumber []string
	sr, err := pj.Stream(context.Background(), Pertanyaan{
		Teks:     "tanya",
		OnSumber: func(s []string) { sumber = s },
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	if got := kurasStream(t, sr); len(got) != 1 || got[0] != "dari linear" {
		t.Errorf("potongan = %v, mau [dari linear]", got)
	}
	if len(sumber) != 1 || sumber[0] != "Pasal 9" {
		t.Errorf("sumber = %v, mau [Pasal 9] (dari cadangan)", sumber)
	}
}

// TestCadanganJatuhKeLinearSaatGagalSebelumPotonganPertama adalah inti
// degradasi anggun: kegagalan yang terjadi setelah agen sempat retrieval tetapi
// sebelum satu pun teks terkirim harus ditutup dengan jawaban dari jalur linear.
//
// Callback agen yang terlanjur menyala juga tidak boleh bocor — kalau bocor,
// pengguna akan melihat rujukan agen untuk jawaban yang sebenarnya datang dari
// jalur linear.
func TestCadanganJatuhKeLinearSaatGagalSebelumPotonganPertama(t *testing.T) {
	utama := &fakePenjawab{
		konteks:            "konteks agen",
		sumber:             []string{"Pasal 7"},
		errAlir:            errors.New("agen putus sebelum menulis"),
		callbackSaatStream: true,
	}
	cadangan := &fakePenjawab{
		potongan:           []string{"dari linear"},
		konteks:            "konteks linear",
		sumber:             []string{"Pasal 9"},
		callbackSaatStream: true,
	}
	pj := &penjawabCadangan{utama: utama, cadangan: cadangan}

	var sumber []string
	sr, err := pj.Stream(context.Background(), Pertanyaan{
		Teks:     "tanya",
		OnSumber: func(s []string) { sumber = s },
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	if got := kurasStream(t, sr); len(got) != 1 || got[0] != "dari linear" {
		t.Errorf("potongan = %v, mau [dari linear]", got)
	}
	if len(sumber) != 1 || sumber[0] != "Pasal 9" {
		t.Errorf("sumber = %v, mau [Pasal 9] dari cadangan, bukan [Pasal 7] dari agen", sumber)
	}
}

// TestCadanganTidakJatuhSetelahPotonganPertama memaku batas kemampuan
// degradasi: begitu satu potongan terkirim, jawaban tidak bisa ditarik lagi,
// jadi error diteruskan apa adanya alih-alih dijawab ulang.
func TestCadanganTidakJatuhSetelahPotonganPertama(t *testing.T) {
	utama := &fakePenjawab{
		potongan: []string{"satu"},
		errAlir:  errors.New("agen putus di tengah"),
	}
	cadangan := &fakePenjawab{potongan: []string{"cadangan"}}
	pj := &penjawabCadangan{utama: utama, cadangan: cadangan}

	sr, err := pj.Stream(context.Background(), Pertanyaan{Teks: "tanya"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer sr.Close()

	pertama, err := sr.Recv()
	if err != nil {
		t.Fatalf("Recv pertama: %v", err)
	}
	if pertama != "satu" {
		t.Errorf("potongan pertama = %q, mau %q", pertama, "satu")
	}

	_, err = sr.Recv()
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("Recv kedua err = %v, mau error agen diteruskan", err)
	}
	if cadangan.jumlahStream != 0 {
		t.Errorf("cadangan dipanggil %d kali, mau 0 (tidak boleh menyusul setelah teks terkirim)", cadangan.jumlahStream)
	}
}

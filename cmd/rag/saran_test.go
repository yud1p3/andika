package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"rag-eino/internal/web"
)

// fakeChatSaran adalah chat model palsu untuk menguji jalur saran tanpa API key.
//
// Hanya Generate yang dipakai rag.Saran; Stream tidak pernah dipanggil, tapi
// tetap harus ada supaya tipe ini memenuhi model.BaseChatModel.
type fakeChatSaran struct {
	balasan string
	err     error
	// blokir membuat Generate menunggu context dibatalkan sebelum kembali,
	// untuk menguji pembatalan di tengah jalan.
	blokir bool

	mu     sync.Mutex
	jumlah int
	// pesan mencatat pesan yang diterima Generate, supaya test bisa memeriksa
	// apa yang benar-benar dikirim ke model.
	pesan [][]*schema.Message
}

func (f *fakeChatSaran) Generate(ctx context.Context, in []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	f.jumlah++
	f.pesan = append(f.pesan, in)
	f.mu.Unlock()

	if f.blokir {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	return schema.AssistantMessage(f.balasan, nil), nil
}

func (f *fakeChatSaran) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("Stream tidak dipakai di test ini")
}

func (f *fakeChatSaran) panggilan() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jumlah
}

// pesanTerakhir mengembalikan pesan yang diterima pada panggilan terakhir.
func (f *fakeChatSaran) pesanTerakhir(t *testing.T) []*schema.Message {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.pesan) == 0 {
		t.Fatal("model tidak pernah dipanggil")
	}
	return f.pesan[len(f.pesan)-1]
}

// bacaSaran membaca satu isi dari saluran saran, dengan batas waktu supaya test
// tidak menggantung kalau ada yang salah.
func bacaSaran(t *testing.T, ch <-chan []string) ([]string, bool) {
	t.Helper()

	select {
	case v, ok := <-ch:
		return v, ok
	case <-time.After(3 * time.Second):
		t.Fatal("menunggu saran melebihi batas waktu")
		return nil, false
	}
}

// pastikanTertutup memastikan saluran saran sudah ditutup, dengan batas waktu
// karena penutupan terjadi di goroutine lain.
func pastikanTertutup(t *testing.T, ch <-chan []string) {
	t.Helper()

	select {
	case v, ok := <-ch:
		if ok {
			t.Errorf("saluran masih berisi %q, mau tertutup", v)
		}
	case <-time.After(2 * time.Second):
		t.Error("saluran tidak ditutup")
	}
}

// TestSaranBersamaanMengumpulkanHasil menguji jalur bahagia: konteks memicu
// penyusunan saran, hasilnya mengalir lewat saluran, lalu saluran ditutup.
func TestSaranBersamaanMengumpulkanHasil(t *testing.T) {
	cm := &fakeChatSaran{balasan: "Apa syaratnya?\nBerapa lama prosesnya?\nSiapa penandatangannya?"}

	mulai, saluran := saranBersamaan(context.Background(), cm, "tanya", 3, nil)
	mulai("Pasal 7 mengatur jenis naskah dinas.")

	hasil, ok := bacaSaran(t, saluran)
	if !ok {
		t.Fatal("saluran tertutup tanpa isi")
	}

	mau := []string{"Apa syaratnya?", "Berapa lama prosesnya?", "Siapa penandatangannya?"}
	if len(hasil) != len(mau) {
		t.Fatalf("dapat %q, mau %q", hasil, mau)
	}
	for i := range mau {
		if hasil[i] != mau[i] {
			t.Errorf("saran ke-%d = %q, mau %q", i, hasil[i], mau[i])
		}
	}

	pastikanTertutup(t, saluran)

	if n := cm.panggilan(); n != 1 {
		t.Errorf("model dipanggil %d kali, mau 1", n)
	}
}

// TestSaranBersamaanTanpaKonteksMenutupKanal memastikan konteks kosong tidak
// memanggil model dan tidak menggantung pembaca.
func TestSaranBersamaanTanpaKonteksMenutupKanal(t *testing.T) {
	for _, konteks := range []string{"", "   ", "\n\t "} {
		t.Run("konteks="+konteks, func(t *testing.T) {
			cm := &fakeChatSaran{balasan: "tidak boleh dipakai"}

			mulai, saluran := saranBersamaan(context.Background(), cm, "tanya", 3, nil)
			mulai(konteks)

			pastikanTertutup(t, saluran)

			if n := cm.panggilan(); n != 0 {
				t.Errorf("model dipanggil %d kali, mau 0", n)
			}
		})
	}
}

// TestSaranBersamaanMematikanSaatJumlahNol memastikan SARAN_COUNT=0 tidak
// memanggil model dan salurannya langsung tertutup.
func TestSaranBersamaanMematikanSaatJumlahNol(t *testing.T) {
	for _, jumlah := range []int{0, -1} {
		t.Run("jumlah="+strconv.Itoa(jumlah), func(t *testing.T) {
			cm := &fakeChatSaran{balasan: "tidak boleh dipakai"}

			mulai, saluran := saranBersamaan(context.Background(), cm, "tanya", jumlah, nil)
			// mulai tidak boleh panik walau fitur mati.
			mulai("Pasal 7")

			pastikanTertutup(t, saluran)

			if n := cm.panggilan(); n != 0 {
				t.Errorf("model dipanggil %d kali, mau 0", n)
			}
		})
	}
}

// TestSaranBersamaanSekaliJalan memastikan callback konteks yang terpanggil
// lebih dari sekali tidak menjalankan model dua kali.
func TestSaranBersamaanSekaliJalan(t *testing.T) {
	cm := &fakeChatSaran{balasan: "Apa syaratnya?"}

	mulai, saluran := saranBersamaan(context.Background(), cm, "tanya", 3, nil)
	mulai("konteks pertama")
	mulai("konteks kedua")

	hasil, ok := bacaSaran(t, saluran)
	if !ok || len(hasil) != 1 {
		t.Fatalf("dapat %q (ok=%v), mau satu saran", hasil, ok)
	}

	pastikanTertutup(t, saluran)

	if n := cm.panggilan(); n != 1 {
		t.Errorf("model dipanggil %d kali, mau 1", n)
	}
}

// TestSaranBersamaanDibatalkan memastikan context yang dibatalkan menghentikan
// penyusunan saran tanpa menggantung pembaca.
func TestSaranBersamaanDibatalkan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cm := &fakeChatSaran{blokir: true}

	mulai, saluran := saranBersamaan(ctx, cm, "tanya", 3, nil)
	mulai("Pasal 7")

	cancel()

	pastikanTertutup(t, saluran)
}

// TestSaranBersamaanModelGagalMenutupKanal memastikan kegagalan model tidak
// menggantung pembaca dan tidak menulis apa pun.
func TestSaranBersamaanModelGagalMenutupKanal(t *testing.T) {
	cm := &fakeChatSaran{err: errors.New("kuota habis")}

	mulai, saluran := saranBersamaan(context.Background(), cm, "tanya", 3, nil)
	mulai("Pasal 7")

	pastikanTertutup(t, saluran)
}

// TestPertanyaanUserMengambilHanyaPertanyaan memastikan riwayat disaring ke
// giliran pengguna saja, dan urutannya tetap.
func TestPertanyaanUserMengambilHanyaPertanyaan(t *testing.T) {
	riwayat := []web.Pesan{
		{Peran: web.PeranUser, Isi: "tanya satu"},
		{Peran: web.PeranAssistant, Isi: "jawaban satu"},
		{Peran: web.PeranUser, Isi: "tanya dua"},
		{Peran: web.PeranAssistant, Isi: "jawaban dua"},
	}

	got := pertanyaanUser(riwayat)
	mau := []string{"tanya satu", "tanya dua"}

	if len(got) != len(mau) {
		t.Fatalf("pertanyaanUser = %q, mau %q", got, mau)
	}
	for i := range mau {
		if got[i] != mau[i] {
			t.Errorf("pertanyaan ke-%d = %q, mau %q", i, got[i], mau[i])
		}
	}
}

// TestPertanyaanUserRiwayatKosong memastikan percakapan baru tidak menghasilkan
// daftar berisi string kosong.
func TestPertanyaanUserRiwayatKosong(t *testing.T) {
	for _, riwayat := range [][]web.Pesan{nil, {}} {
		if got := pertanyaanUser(riwayat); got != nil {
			t.Errorf("pertanyaanUser(%v) = %q, mau nil", riwayat, got)
		}
	}
}

// TestSaranBersamaanMeneruskanPertanyaanLama memastikan pertanyaan lama yang
// diterima saranBersamaan benar-benar sampai ke prompt model.
//
// Ini penjaga regresi untuk sambungan antara lapisan web dan rag: parameter
// yang lupa diteruskan tidak memicu error apa pun — yang terjadi hanya tombol
// saran yang kembali menawarkan hal yang sudah dijawab.
func TestSaranBersamaanMeneruskanPertanyaanLama(t *testing.T) {
	cm := &fakeChatSaran{balasan: "Apa syaratnya?"}

	const lama = "Apa itu lembar disposisi?"

	mulai, saluran := saranBersamaan(context.Background(), cm, "tanya", 3, []string{lama})
	mulai("Pasal 7 mengatur disposisi.")
	bacaSaran(t, saluran)

	pesan := cm.pesanTerakhir(t)
	if len(pesan) != 2 {
		t.Fatalf("model menerima %d pesan, mau 2 (sistem + user)", len(pesan))
	}
	if !strings.Contains(pesan[1].Content, lama) {
		t.Errorf("pesan user tidak memuat pertanyaan lama %q:\n%s", lama, pesan[1].Content)
	}
}

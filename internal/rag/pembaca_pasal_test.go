package rag

import (
	"reflect"
	"testing"
)

// TestKunciUrutanMengenaliHeader mengunci pembacaan posisi dari header buatan
// ingest. Formatnya ditentukan internal/ingest/pasal.go; kalau salah satu
// bentuk berubah, pengurutan isi pasal ikut rusak tanpa error apa pun.
func TestKunciUrutanMengenaliHeader(t *testing.T) {
	kasus := []struct {
		nama string
		isi  string
		rank int
		num  int
		sub  int
	}{
		{"ayat", "Pasal 7 ayat (3)\n\n- (3) ketiga", 1, 3, 0},
		{"rentang ayat", "Pasal 7 ayat (2)-(4)\n\n- (2) kedua", 1, 2, 0},
		{"butir", "Pasal 1 angka (5)-(7)\n\n- 5. definisi", 1, 5, 0},
		{"bagian", "Pasal 9 (bagian 2/5)\n\nte x", 0, 2, 0},
		{"ayat dengan bagian", "Pasal 7 ayat (5), bagian 2/5\n\nte x", 1, 5, 2},
		{"potongan tunggal", "Pasal 7 Jenis Naskah Dinas\n\nterdiri atas:", 0, 0, 0},
	}

	for _, k := range kasus {
		t.Run(k.nama, func(t *testing.T) {
			rank, num, sub := kunciUrutan(k.isi)
			if rank != k.rank || num != k.num || sub != k.sub {
				t.Errorf("kunciUrutan = (%d,%d,%d), mau (%d,%d,%d)",
					rank, num, sub, k.rank, k.num, k.sub)
			}
		})
	}
}

// TestUrutkanPotonganMengembalikanUrutanDokumen meniru hasil Scroll Qdrant yang
// urutannya tidak berarti, lalu memastikan isinya kembali ke urutan dokumen.
//
// Teks pengantar harus mendahului ayat, walaupun nomor bagiannya bisa sama
// dengan nomor ayat pertama — karena itulah rank dipisah dari nomor.
func TestUrutkanPotonganMengembalikanUrutanDokumen(t *testing.T) {
	potongan := []string{
		"Pasal 7 ayat (5)\n\n- (5) kelima",
		"Pasal 7 (bagian 1/3)\n\npengantar",
		"Pasal 7 ayat (3)-(4)\n\n- (3) ketiga",
		"Pasal 7 ayat (1)-(2)\n\n- (1) pertama",
	}

	mau := []string{
		"Pasal 7 (bagian 1/3)\n\npengantar",
		"Pasal 7 ayat (1)-(2)\n\n- (1) pertama",
		"Pasal 7 ayat (3)-(4)\n\n- (3) ketiga",
		"Pasal 7 ayat (5)\n\n- (5) kelima",
	}

	urutkanPotongan(potongan)

	if !reflect.DeepEqual(potongan, mau) {
		t.Errorf("urutan salah:\n dapat %v\n mau   %v", potongan, mau)
	}
}

// TestUrutkanPotonganMengurutkanSubBagian menguji satu ayat yang masih dipecah
// lagi: nomor bagiannya dipakai sebagai kunci ketiga.
func TestUrutkanPotonganMengurutkanSubBagian(t *testing.T) {
	potongan := []string{
		"Pasal 9 ayat (5), bagian 2/2\n\nlanjutan",
		"Pasal 9 ayat (5), bagian 1/2\n\nawal",
		"Pasal 9 ayat (1)\n\n- (1)",
	}

	mau := []string{
		"Pasal 9 ayat (1)\n\n- (1)",
		"Pasal 9 ayat (5), bagian 1/2\n\nawal",
		"Pasal 9 ayat (5), bagian 2/2\n\nlanjutan",
	}

	urutkanPotongan(potongan)

	if !reflect.DeepEqual(potongan, mau) {
		t.Errorf("urutan salah:\n dapat %v\n mau   %v", potongan, mau)
	}
}

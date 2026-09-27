package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// bacaPasalTetap adalah pembaca pasal palsu yang mengembalikan satu potongan
// tetap, sehingga tes tidak menyentuh Qdrant.
func bacaPasalTetap(_ context.Context, nomor string) ([]string, error) {
	return []string{"Pasal " + nomor + " isi uji."}, nil
}

// newTestServer menyiapkan server dengan penjawab palsu, sehingga tes tidak
// menyentuh Gemini maupun Qdrant.
func newTestServer(t *testing.T, alirkan Alirkan) *Server {
	t.Helper()

	srv, err := New(alirkan, bacaPasalTetap, Tampilan{
		Title:      "Tanya Uji",
		Subtitle:   "Sumber: dokumen uji",
		Model:      "gemini-uji",
		Collection: "koleksi-uji",
		TopK:       3,
	}, nil)
	if err != nil {
		t.Fatalf("membuat server: %v", err)
	}
	return srv
}

// serve menjalankan satu permintaan GET dan mengembalikan perekamnya.
//
// Tidak pernah memanggil t.Fatalf, sehingga aman dipakai dari goroutine lain —
// misalnya saat menguji permintaan yang datang bersamaan.
func serve(srv *Server, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// panggil menjalankan satu permintaan GET tanpa cookie.
func panggil(t *testing.T, srv *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	return serve(srv, target, nil)
}

// panggilDenganCookie menjalankan satu permintaan GET memakai cookie sesi.
func panggilDenganCookie(t *testing.T, srv *Server, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	return serve(srv, target, cookie)
}

// postBaru menjalankan POST /baru dengan cookie sesi.
func postBaru(srv *Server, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/baru", nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

// cookieSesiMuat membuka halaman utama dan mengembalikan cookie sesi yang
// disetel server.
func cookieSesiMuat(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()

	rec := panggil(t, srv, "/")

	for _, c := range rec.Result().Cookies() {
		if c.Name == namaCookieSesi {
			return c
		}
	}

	t.Fatalf("GET / tidak menyetel cookie %s", namaCookieSesi)
	return nil
}

// alirkanTetap adalah penjawab palsu yang mengirim potongan lalu berhasil.
func alirkanTetap(potongan ...string) Alirkan {
	return func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		for _, p := range potongan {
			if err := delta(p); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
}

// alirkanSaran adalah penjawab palsu yang mengirim jawaban lalu menyerahkan
// saran lewat saluran, meniru saran yang sudah siap saat jawaban habis.
func alirkanSaran(potongan string, saran ...string) Alirkan {
	return func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		if err := delta(potongan); err != nil {
			return nil, err
		}

		ch := make(chan []string, 1)
		ch <- saran
		close(ch)
		return ch, nil
	}
}

func TestIndexMenyajikanHalaman(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("apa saja")), "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}

	isi := rec.Body.String()
	for _, wajib := range []string{
		"/statik/htmx.min.js",
		"/statik/sse.min.js",
		"/statik/app.css",
		`hx-get="/tanya"`,
		// Tombol "Percakapan baru" mengosongkan riwayat sesi.
		`hx-post="/baru"`,
		// Judul dan keterangan datang dari konfigurasi, bukan ditulis di
		// template — jadi mengganti dokumen sumber tidak perlu ubah HTML.
		"Tanya Uji",
		"Sumber: dokumen uji",
		"gemini-uji",
		"koleksi-uji",
	} {
		if !strings.Contains(isi, wajib) {
			t.Errorf("halaman tidak memuat %q", wajib)
		}
	}
}

// TestIndexMenyajikanPertanyaanContoh menjaga tombol pertanyaan siap-klik.
//
// Banyak pengunjung tidak tahu dokumennya memuat apa, jadi pertanyaan pertama
// yang paling sulit dijawab adalah "mau tanya apa". Tombolnya harus benar-benar
// menjalankan pertanyaan itu.
//
// Dua hal mudah rusak dan tidak terlihat sampai diklik:
//
//   - type="button" hilang, sehingga peramban menganggapnya tombol kirim dan
//     formulir ikut terkirim — satu klik jadi dua permintaan.
//   - hx-vals rusak karena tanda kutip gandanya di-escape template, sehingga
//     htmx gagal mengurainya dan pertanyaannya tidak terkirim sama sekali.
func TestIndexMenyajikanPertanyaanContoh(t *testing.T) {
	isi := panggil(t, newTestServer(t, alirkanTetap("x")), "/").Body.String()

	// Pertanyaan contoh harus muncul sebagai label tombol sekaligus sebagai
	// isi hx-vals; keduanya diperiksa lewat daftar yang sama supaya tidak
	// mungkin hanya salah satunya yang ikut berubah.
	contoh := []string{
		"Jenis naskah dinas apa saja?",
		"Siapa yang berwenang menandatangani surat dinas?",
		"Bagaimana aturan penomoran naskah dinas?",
		"Apa yang dimaksud dengan lembar disposisi?",
	}

	// Spasi dirapatkan untuk pemeriksaan label: teks tombol ditulis di baris
	// sendiri di dalam template, jadi selalu didahului baris baru dan indentasi.
	rapat := strings.Join(strings.Fields(isi), " ")

	for _, q := range contoh {
		if !strings.Contains(rapat, "> "+q) {
			t.Errorf("tidak ada tombol dengan label %q", q)
		}

		vals, ok := semuaAtribut(isi, "hx-vals")
		if !ok {
			t.Fatal(`tidak ada atribut hx-vals di halaman`)
		}

		// Semua hx-vals dikumpulkan, lalu diurai sebagai JSON. Kalau escape
		// template merusaknya, json.Unmarshal gagal di sini — jauh sebelum
		// pengguna menemukan tombol yang tidak bereaksi.
		var terurai bool
		for _, v := range vals {
			var isi map[string]string
			if err := json.Unmarshal([]byte(v), &isi); err != nil {
				t.Errorf("hx-vals %q bukan JSON yang sah: %v", v, err)
				continue
			}
			if isi["q"] == q {
				terurai = true
			}
		}
		if !terurai {
			t.Errorf("tidak ada hx-vals yang mengirim pertanyaan %q; yang ada: %v", q, vals)
		}
	}

	if n := strings.Count(isi, `type="button"`); n < len(contoh) {
		t.Errorf("tombol bertipe button hanya %d, mau minimal %d", n, len(contoh))
	}
}

// TestIndexMenyembunyikanPertanyaanContohSetelahDijawab menjaga agar tombol
// pertanyaan contoh tidak menetap di bawah kotak pencarian setelah percakapan
// dimulai.
//
// Patokannya keberadaan <article> di #log, bukan "apakah #log kosong": balasan
// "Percakapan baru" berupa <p>, jadi #log tidak pernah benar-benar kosong
// setelah dipakai. Yang membedakan percakapan baru dari percakapan berisi
// adalah ada tidaknya kartu pertanyaan.
//
// Yang diuji di sini adalah wiring-nya, bukan perilaku peramban: halaman harus
// memberi wadah tombol sebuah id, dan #log harus punya penghalau yang mencari
// <article> lalu menyembunyikan wadah itu. Kalau salah satunya hilang, tombol
// contoh akan menetap selamanya tanpa ada error di mana pun.
func TestIndexMenyembunyikanPertanyaanContohSetelahDijawab(t *testing.T) {
	isi := panggil(t, newTestServer(t, alirkanTetap("x")), "/").Body.String()

	if !strings.Contains(isi, `id="contoh"`) {
		t.Error(`wadah tombol pertanyaan contoh tidak punya id="contoh"`)
	}

	pengendali, ok := semuaAtribut(isi, "hx-on::after-swap")
	if !ok {
		t.Fatal("tidak ada atribut hx-on::after-swap di halaman")
	}

	var ketemu bool
	for _, v := range pengendali {
		if strings.Contains(v, "contoh") && strings.Contains(v, "article") {
			ketemu = true
		}
	}
	if !ketemu {
		t.Errorf("hx-on::after-swap tidak menyembunyikan #contoh saat ada <article>; yang ada: %v", pengendali)
	}
}

// TestIndexMenghapusPesanPercakapanBaruSetelahDijawab menjaga agar pengumuman
// "Percakapan baru dimulai" tidak menetap di atas percakapan.
//
// Pengumuman itu disisipkan sebagai isi #log, sedangkan pertanyaan berikutnya
// di-append dengan beforeend — jadi tanpa dihapus ia akan bertahan selamanya di
// atas kartu pertanyaan, padahal maksudnya hanya berlaku sampai pertanyaan itu
// dikirim.
//
// Yang diuji di sini adalah wiring-nya, bukan perilaku peramban: #log harus
// punya penghalau yang mencari #pesan-baru lalu menghapusnya. Kalau atribut
// hx-on::after-swap atau id-nya hilang, pengumuman akan menetap tanpa error di
// mana pun.
func TestIndexMenghapusPesanPercakapanBaruSetelahDijawab(t *testing.T) {
	isi := panggil(t, newTestServer(t, alirkanTetap("x")), "/").Body.String()

	pengendali, ok := semuaAtribut(isi, "hx-on::after-swap")
	if !ok {
		t.Fatal("tidak ada atribut hx-on::after-swap di halaman")
	}

	var ketemu bool
	for _, v := range pengendali {
		if strings.Contains(v, "pesan-baru") && strings.Contains(v, "remove") {
			ketemu = true
		}
	}
	if !ketemu {
		t.Errorf("hx-on::after-swap tidak menghapus #pesan-baru; yang ada: %v", pengendali)
	}
}

// semuaAtribut mengumpulkan nilai semua atribut bernama itu di dalam halaman.
//
// Berbeda dari ambilAtribut yang berhenti di kemunculan pertama: di sini yang
// dicari justru seluruhnya, karena tiap tombol pertanyaan contoh punya
// hx-vals-nya sendiri dan semuanya harus ikut diperiksa.
//
// Nilainya dikembalikan apa adanya, belum di-unescape, karena yang diuji
// justru bentuk mentahnya: kalau templating merusak tanda kutipnya, hasil
// unescape akan menutupi kerusakan itu.
func semuaAtribut(halaman, nama string) ([]string, bool) {
	var hasil []string

	for sisa := halaman; ; {
		i := strings.Index(sisa, nama+"=")
		if i < 0 {
			break
		}
		sisa = sisa[i+len(nama)+1:]

		if len(sisa) == 0 || (sisa[0] != '"' && sisa[0] != '\'') {
			continue
		}
		penutup := sisa[0]
		sisa = sisa[1:]

		j := strings.IndexByte(sisa, penutup)
		if j < 0 {
			break
		}
		hasil = append(hasil, sisa[:j])
		sisa = sisa[j+1:]
	}

	return hasil, len(hasil) > 0
}

// TestIndexMenempelkanFormulirDiBawah menjaga tata letak kotak pertanyaan.
//
// Kotak pertanyaan harus berada di luar dan sesudah #log, sementara #log
// sendiri yang menggulir. Kalau #log tidak dibatasi tingginya (min-h-0), ia
// memanjang mengikuti isinya, mendorong formulir keluar layar, dan pengguna
// harus menggulir balik untuk bertanya lagi — persis yang ingin dihindari.
func TestIndexMenempelkanFormulirDiBawah(t *testing.T) {
	isi := panggil(t, newTestServer(t, alirkanTetap("x")), "/").Body.String()

	// Tinggi halaman harus dikunci ke tinggi layar, bukan tinggi isi.
	if tag := ambilTag(t, isi, "<body"); !strings.Contains(tag, "h-dvh") {
		t.Errorf("body tidak dikunci ke tinggi layar:\n%s", tag)
	}

	iLog := strings.Index(isi, `id="log"`)
	if iLog < 0 {
		t.Fatalf("tidak ada #log:\n%s", isi)
	}

	iForm := strings.Index(isi, "<form")
	if iForm < 0 {
		t.Fatalf("tidak ada formulir:\n%s", isi)
	}
	if iForm < iLog {
		t.Errorf("formulir mendahului #log, sehingga ikut tergulir ke atas")
	}

	// #log adalah satu-satunya bagian yang menggulir.
	log := ambilTag(t, isi, `id="log"`)
	for _, wajib := range []string{"overflow-y-auto", "min-h-0", "flex-1"} {
		if !strings.Contains(log, wajib) {
			t.Errorf("tag #log tidak memuat %q:\n%s", wajib, log)
		}
	}

	// Fragmen pertanyaan baru harus langsung terlihat setelah disisipkan.
	if tag := ambilTag(t, isi, "<form"); !strings.Contains(tag, "show:bottom") {
		t.Errorf("formulir tidak menggulirkan #log ke bawah:\n%s", tag)
	}
}

// TestAsetStatisTersedia memastikan berkas yang di-embed benar-benar ada.
// app.css adalah hasil build Tailwind; kalau lupa menjalankan npm run build:css,
// berkasnya tidak ada dan embed akan gagal — tes ini menangkapnya.
func TestAsetStatisTersedia(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("apa saja"))

	for _, berkas := range []string{"app.css", "htmx.min.js", "sse.min.js"} {
		rec := panggil(t, srv, "/statik/"+berkas)

		if rec.Code != http.StatusOK {
			t.Errorf("/statik/%s status = %d, mau %d", berkas, rec.Code, http.StatusOK)
			continue
		}
		if rec.Body.Len() == 0 {
			t.Errorf("/statik/%s kosong", berkas)
		}
	}
}

func TestTanyaMengembalikanFragmen(t *testing.T) {
	pertanyaan := `Apa itu "MPASI" & kapan?`

	rec := panggil(t, newTestServer(t, alirkanTetap("x")), "/tanya?q="+url.QueryEscape(pertanyaan))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}

	isi := rec.Body.String()

	// Pertanyaan ditampilkan sebagai teks, jadi karakter khusus harus ter-escape
	// supaya tidak bisa menyuntikkan tag.
	if strings.Contains(isi, `"MPASI" &`) {
		t.Errorf("pertanyaan tidak ter-escape:\n%s", isi)
	}
	if !strings.Contains(isi, "&amp;") {
		t.Errorf("halaman tidak memuat pertanyaan ter-escape:\n%s", isi)
	}

	// Elemen SSE harus lengkap: hx-ext sepasang dengan sse-connect di satu tag,
	// dan sse-close wajib ada supaya koneksi tidak disambung ulang.
	for _, wajib := range []string{`hx-ext="sse"`, "sse-connect=", `sse-close="tutup"`, `sse-swap="delta"`} {
		if !strings.Contains(isi, wajib) {
			t.Errorf("fragmen tidak memuat %q:\n%s", wajib, isi)
		}
	}

	// Pertanyaan ikut di URL koneksi SSE karena EventSource hanya bisa GET.
	// Dibandingkan lewat uji bolak-balik: ambil nilai atributnya, unescape
	// seperti yang dilakukan browser, lalu baca query-nya. Perbandingan
	// substring mentah tidak bisa dipakai di sini karena html/template
	// meng-escape "+" menjadi "&#43;" di dalam nilai atribut.
	urlSSE := ambilAtribut(t, isi, "sse-connect")

	u, err := url.Parse(urlSSE)
	if err != nil {
		t.Fatalf("URL sse-connect tidak bisa dibaca (%q): %v", urlSSE, err)
	}
	if got := u.Query().Get("q"); got != pertanyaan {
		t.Errorf("q pada sse-connect = %q, mau %q", got, pertanyaan)
	}

	// Kedua wadah yang menerima swap dari SSE harus menggulirkan hasilnya ke
	// bawah. Isinya bertambah selama jawaban mengalir, dan menambah isi tidak
	// menggeser posisi gulir — tanpa show:bottom jawaban memanjang ke luar
	// layar dan pembaca harus menggulir sendiri mengikuti tiap potongan.
	for _, penanda := range []string{`sse-swap="selesai"`, `sse-swap="delta"`} {
		if tag := ambilTag(t, isi, penanda); !strings.Contains(tag, "show:bottom") {
			t.Errorf("elemen %s tidak memakai show:bottom:\n%s", penanda, tag)
		}
	}
}

// ambilAtribut mengambil nilai sebuah atribut HTML dan mengembalikannya setelah
// di-unescape, yaitu bentuk yang benar-benar dibaca browser.
func ambilAtribut(t *testing.T, dokumen, nama string) string {
	t.Helper()

	awal := nama + `="`
	i := strings.Index(dokumen, awal)
	if i < 0 {
		t.Fatalf("atribut %s tidak ditemukan:\n%s", nama, dokumen)
	}

	sisa := dokumen[i+len(awal):]

	akhir := strings.Index(sisa, `"`)
	if akhir < 0 {
		t.Fatalf("atribut %s tidak ditutup:\n%s", nama, dokumen)
	}

	return html.UnescapeString(sisa[:akhir])
}

// ambilTag mengembalikan teks satu tag HTML yang memuat penanda, misalnya
// `id="log"`. Dipakai untuk memeriksa kelas pada satu elemen tertentu.
func ambilTag(t *testing.T, dokumen, penanda string) string {
	t.Helper()

	i := strings.Index(dokumen, penanda)
	if i < 0 {
		t.Fatalf("penanda %q tidak ditemukan:\n%s", penanda, dokumen)
	}

	awal := strings.LastIndex(dokumen[:i], "<")
	akhir := strings.Index(dokumen[i:], ">")
	if awal < 0 || akhir < 0 {
		t.Fatalf("tag di sekitar %q tidak lengkap:\n%s", penanda, dokumen)
	}

	return dokumen[awal : i+akhir+1]
}

func TestPertanyaanKosongDitolak(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("x"))

	for _, target := range []string{"/tanya", "/tanya?q=", "/tanya/alir", "/tanya/alir?q=%20%20"} {
		rec := panggil(t, srv, target)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, mau %d", target, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestAlirMengirimEventBertahap(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("MPASI ", "mulai ", "usia 6 bulan.")),
		"/tanya/alir?q=kapan+MPASI%3F")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, mau text/event-stream", ct)
	}

	isi := rec.Body.String()

	if n := strings.Count(isi, "event: delta"); n != 3 {
		t.Errorf("jumlah event delta = %d, mau 3:\n%s", n, isi)
	}

	// Setiap potongan harus jadi satu pesan data tersendiri.
	for _, potongan := range []string{"data: MPASI \n", "data: mulai \n", "data: usia 6 bulan.\n"} {
		if !strings.Contains(isi, potongan) {
			t.Errorf("respons tidak memuat %q:\n%s", potongan, isi)
		}
	}

	// Urutan penting: penutup harus datang setelah HTML final, kalau tidak
	// koneksi ditutup sebelum jawaban terakhir sempat ditukar.
	iDelta := strings.Index(isi, "event: delta")
	iSelesai := strings.Index(isi, "event: selesai")
	iTutup := strings.Index(isi, "event: tutup")

	if iSelesai < 0 {
		t.Fatalf("tidak ada event selesai:\n%s", isi)
	}
	if iTutup < 0 {
		t.Fatalf("tidak ada event tutup (koneksi akan disambung ulang):\n%s", isi)
	}
	if !(iDelta < iSelesai && iSelesai < iTutup) {
		t.Errorf("urutan event salah (delta=%d selesai=%d tutup=%d):\n%s", iDelta, iSelesai, iTutup, isi)
	}

	// HTML final adalah hasil render Markdown dari gabungan potongan.
	if !strings.Contains(isi, "<p>MPASI mulai usia 6 bulan.</p>") {
		t.Errorf("HTML final tidak berisi jawaban utuh:\n%s", isi)
	}
}

// TestAlirMengirimRujukanSetelahJawaban mengunci dua hal yang membuat rujukan
// berguna.
//
// Pertama, ia dikirim sebagai event tersendiri — bukan disisipkan ke teks
// jawaban — sehingga tidak ikut masuk ke riwayat percakapan dan tidak terbaca
// model sebagai bagian dari jawabannya sendiri pada giliran berikutnya.
//
// Kedua, ia datang setelah potongan jawaban terakhir, tetapi tetap sebelum
// koneksi ditutup. Rujukan baru bisa disaring setelah jawabannya utuh (lihat
// rag.SaringSumber), jadi adapter mengirimnya di ujung aliran.
func TestAlirMengirimRujukanSetelahJawaban(t *testing.T) {
	alirkan := func(_ context.Context, _ string, _ []Pesan,
		delta func(string) error, sumber func([]string) error) (SaluranSaran, error) {
		if err := delta("Jenis naskah dinas ada tiga."); err != nil {
			return nil, err
		}
		return nil, sumber([]string{"Pasal 7", "Lampiran"})
	}

	rec := panggil(t, newTestServer(t, alirkan), "/tanya/alir?q=x")
	isi := rec.Body.String()

	if !strings.Contains(isi, "event: sumber") {
		t.Fatalf("tidak ada event sumber:\n%s", isi)
	}

	iSumber := strings.Index(isi, "event: sumber")
	iDelta := strings.Index(isi, "event: delta")
	if iDelta < 0 {
		t.Fatalf("tidak ada event delta:\n%s", isi)
	}
	if iSumber < iDelta {
		t.Errorf("event sumber datang sebelum delta (sumber=%d delta=%d):\n%s", iSumber, iDelta, isi)
	}

	iTutup := strings.Index(isi, "event: tutup")
	if iTutup < 0 {
		t.Fatalf("tidak ada event tutup:\n%s", isi)
	}
	if iSumber > iTutup {
		t.Errorf("event sumber datang setelah tutup (sumber=%d tutup=%d):\n%s", iSumber, iTutup, isi)
	}

	for _, label := range []string{"Pasal 7", "Lampiran"} {
		if !strings.Contains(isi, label) {
			t.Errorf("respons tidak memuat rujukan %q:\n%s", label, isi)
		}
	}

	// Rujukan tidak boleh muncul di dalam HTML jawaban.
	if strings.Contains(isi, "<p>Pasal 7") {
		t.Errorf("rujukan bocor ke dalam teks jawaban:\n%s", isi)
	}
}

// TestAlirTanpaRujukanTidakMengirimEvent menjaga halaman dari blok kosong.
//
// Kalau tidak ada yang bisa dirujuk, event "sumber" tidak dikirim sama sekali —
// kalau dikirim, wadahnya akan terisi pembungkus kosong berisi tulisan
// "Rujukan:" tanpa satu pun label di belakangnya.
func TestAlirTanpaRujukanTidakMengirimEvent(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("jawaban")), "/tanya/alir?q=x")

	if isi := rec.Body.String(); strings.Contains(isi, "event: sumber") {
		t.Errorf("event sumber terkirim padahal tidak ada rujukan:\n%s", isi)
	}
}

// TestAlirMengirimSaranSetelahJawaban mengunci urutan event saran.
//
// Saran baru siap setelah jawaban selesai dikirim, tapi tetap harus terkirim
// sebelum koneksi ditutup — kalau "saran" datang setelah "tutup", htmx sudah
// memutus koneksi dan tombolnya tidak akan pernah muncul.
func TestAlirMengirimSaranSetelahJawaban(t *testing.T) {
	rec := panggil(t,
		newTestServer(t, alirkanSaran("jawaban", "Apa syaratnya?", "Berapa lama prosesnya?")),
		"/tanya/alir?q=x")

	isi := rec.Body.String()

	iSelesai := strings.Index(isi, "event: selesai")
	iSaran := strings.Index(isi, "event: saran")
	iTutup := strings.Index(isi, "event: tutup")

	if iSelesai < 0 || iSaran < 0 || iTutup < 0 {
		t.Fatalf("event tidak lengkap (selesai=%d saran=%d tutup=%d):\n%s", iSelesai, iSaran, iTutup, isi)
	}
	if !(iSelesai < iSaran && iSaran < iTutup) {
		t.Errorf("urutan event salah (selesai=%d saran=%d tutup=%d):\n%s", iSelesai, iSaran, iTutup, isi)
	}

	for _, saran := range []string{"Apa syaratnya?", "Berapa lama prosesnya?"} {
		if !strings.Contains(isi, saran) {
			t.Errorf("respons tidak memuat saran %q:\n%s", saran, isi)
		}
	}

	// Saran tidak boleh bocor ke HTML jawaban: ia tombol, bukan bagian jawaban.
	if strings.Contains(isi, "<p>Apa syaratnya?") {
		t.Errorf("saran bocor ke dalam teks jawaban:\n%s", isi)
	}
}

// TestAlirTanpaSaranTidakMengirimEvent menjaga wadah saran dari blok kosong.
//
// Kalau tidak ada saran — fitur dimatikan, konteks kosong, atau model gagal —
// event "saran" tidak dikirim sama sekali. Kalau dikirim, wadahnya terisi
// pembungkus berisi tulisan "Lanjutkan:" tanpa satu pun tombol.
func TestAlirTanpaSaranTidakMengirimEvent(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("jawaban")), "/tanya/alir?q=x")

	if isi := rec.Body.String(); strings.Contains(isi, "event: saran") {
		t.Errorf("event saran terkirim padahal tidak ada saran:\n%s", isi)
	}
}

// TestAlirTetapMenutupSaatSaranLambat menjaga koneksi tidak menggantung.
//
// Saran adalah tambahan: model yang lambat tidak boleh menahan penutup sampai
// browser menyerah. Batas waktunya dipercepat di sini supaya test tidak ikut
// menunggu tiga detik.
func TestAlirTetapMenutupSaatSaranLambat(t *testing.T) {
	alirkan := func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		if err := delta("jawaban"); err != nil {
			return nil, err
		}
		// Saluran yang tidak pernah diisi dan tidak pernah ditutup: meniru
		// model saran yang belum selesai.
		return make(chan []string), nil
	}

	srv := newTestServer(t, alirkan)
	srv.waktuTungguSaran = 50 * time.Millisecond

	isi := panggil(t, srv, "/tanya/alir?q=x").Body.String()

	if !strings.Contains(isi, "event: tutup") {
		t.Errorf("koneksi tidak ditutup saat saran lambat:\n%s", isi)
	}
	if strings.Contains(isi, "event: saran") {
		t.Errorf("event saran terkirim padahal belum siap:\n%s", isi)
	}
}

// TestAlirTidakMengirimSaranSaatJawabanGagal memastikan percakapan yang
// terhenti tidak ditawari lanjutan.
//
// Pengguna belum sempat membaca jawabannya, jadi menawarkan pertanyaan
// berikutnya hanya membingungkan.
func TestAlirTidakMengirimSaranSaatJawabanGagal(t *testing.T) {
	alirkan := func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		if err := delta("awal "); err != nil {
			return nil, err
		}

		ch := make(chan []string, 1)
		ch <- []string{"Apa syaratnya?"}
		close(ch)
		return ch, errors.New("kuota habis")
	}

	isi := panggil(t, newTestServer(t, alirkan), "/tanya/alir?q=x").Body.String()

	if strings.Contains(isi, "event: saran") {
		t.Errorf("saran terkirim padahal jawaban gagal:\n%s", isi)
	}
	if !strings.Contains(isi, "event: tutup") {
		t.Errorf("koneksi tidak ditutup saat gagal:\n%s", isi)
	}
}

// TestSaranMengescapeTeks menutup celah penyuntikan lewat keluaran model.
//
// Saran berasal dari model dan htmx menukar isi event sebagai HTML, jadi
// teksnya harus di-escape sebelum dirender jadi tombol.
func TestSaranMengescapeTeks(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("x"))

	htmlSaran := srv.renderSaran([]string{`<script>alert(1)</script>`})

	if strings.Contains(htmlSaran, "<script>alert(1)</script>") {
		t.Errorf("teks saran tidak di-escape:\n%s", htmlSaran)
	}
	if !strings.Contains(htmlSaran, "&lt;script&gt;") {
		t.Errorf("teks saran tidak tampil ter-escape:\n%s", htmlSaran)
	}
}

// TestSaranMengirimLewatQueryStringTanya adalah uji regresi untuk pilihan
// query string alih-alih hx-vals.
//
// Saran bisa memuat tanda kutip. Kalau pertanyaannya ditulis di hx-vals berisi
// JSON, html/template meng-escape kutip jadi &#34;, browser meng-decode-nya
// kembali jadi kutip, dan JSON-nya jadi tidak sah — tombolnya tidak bereaksi.
// Query string tidak punya masalah itu, dan test ini membuktikan pertanyaannya
// bolak-balik utuh sampai ke handleTanya.
func TestSaranMengirimLewatQueryStringTanya(t *testing.T) {
	const pertanyaan = `Apa itu "lembar disposisi"?`

	srv := newTestServer(t, alirkanTetap("x"))

	target := ambilAtribut(t, srv.renderSaran([]string{pertanyaan}), "hx-get")

	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("hx-get %q tidak bisa dibaca: %v", target, err)
	}
	if got := u.Query().Get("q"); got != pertanyaan {
		t.Errorf("q pada hx-get = %q, mau %q", got, pertanyaan)
	}

	// Dan handleTanya harus menerimanya bulat sebagai fragmen pertanyaan.
	// Dibandingkan setelah unescape, supaya bentuk escape-nya (&#34; atau
	// &quot;) tidak jadi bagian yang diuji.
	rec := panggil(t, srv, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, mau %d", target, rec.Code, http.StatusOK)
	}
	if teks := html.UnescapeString(rec.Body.String()); !strings.Contains(teks, pertanyaan) {
		t.Errorf("fragmen tidak memuat pertanyaan asli %q:\n%s", pertanyaan, rec.Body.String())
	}
}

// TestRujukanMengescapeLabel menutup celah penyuntikan lewat metadata dokumen.
//
// Label diambil dari metadata potongan, dan htmx menukar isi event sebagai
// HTML. Metadata saat ini selalu tulisan tangan dari ingest, tapi escape-nya
// harus tetap berlaku supaya dokumen lain tidak bisa menyuntikkan markup.
func TestRujukanMengescapeLabel(t *testing.T) {
	alirkan := func(_ context.Context, _ string, _ []Pesan,
		delta func(string) error, sumber func([]string) error) (SaluranSaran, error) {
		if err := sumber([]string{"<script>alert(1)</script>"}); err != nil {
			return nil, err
		}
		return nil, delta("jawaban")
	}

	rec := panggil(t, newTestServer(t, alirkan), "/tanya/alir?q=x")
	isi := rec.Body.String()

	if strings.Contains(isi, "<script>alert(1)</script>") {
		t.Errorf("label tidak di-escape:\n%s", isi)
	}
	if !strings.Contains(isi, "&lt;script&gt;") {
		t.Errorf("label tidak tampil dalam bentuk ter-escape:\n%s", isi)
	}
}

// TestSumberMenjadikanPasalTombol mengunci wiring tombol rujukan.
//
// Label pasal harus memuat hx-get ke /pasal dengan nomornya, dan menargetkan
// wadah isi pasal di kartu yang sama lewat pemilih relatif. Label non-pasal
// tetap label biasa — tidak punya nomor yang bisa diambil.
func TestSumberMenjadikanPasalTombol(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("x"))

	isi := srv.renderSumber([]string{"Pasal 7", "Lampiran"})

	if !strings.Contains(isi, `hx-get="/pasal?nomor=7"`) {
		t.Errorf("tombol pasal tidak mengarah ke /pasal:\n%s", isi)
	}
	if !strings.Contains(isi, `hx-target="next .isi-pasal"`) {
		t.Errorf("tombol pasal tidak menargetkan wadah isi pasal:\n%s", isi)
	}

	// Hanya satu tombol: "Lampiran" tidak boleh ikut bisa diklik.
	if n := strings.Count(isi, "hx-get="); n != 1 {
		t.Errorf("jumlah tombol rujukan = %d, mau 1:\n%s", n, isi)
	}
	if !strings.Contains(isi, ">Lampiran<") {
		t.Errorf("label Lampiran hilang:\n%s", isi)
	}
}

// TestPasalMengembalikanIsi memastikan tombol rujukan benar-benar bisa memuat
// isi pasal.
func TestPasalMengembalikanIsi(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("x")), "/pasal?nomor=7")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}

	isi := rec.Body.String()
	for _, wajib := range []string{"Pasal 7", "isi uji"} {
		if !strings.Contains(isi, wajib) {
			t.Errorf("fragmen tidak memuat %q:\n%s", wajib, isi)
		}
	}
}

// TestPasalTidakMengulangPenanda menjaga panel tidak mencetak "Pasal N" dua
// kali.
//
// Isi potongan sudah membawa penandanya sendiri, jadi judul panel hanya akan
// mengulanginya tepat di atasnya. Itulah yang dulu terjadi: chip "Pasal 7" di
// baris rujukan, lalu judul panel "Pasal 7", lalu isi yang juga mulai dengan
// "Pasal 7".
func TestPasalTidakMengulangPenanda(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("x"))

	isi := srv.renderPasal([]string{
		"Pasal 7 Jenis Naskah Dinas sebagaimana dimaksud dalam Pasal 4 huruf a terdiri atas:",
	})

	if n := strings.Count(isi, "Pasal 7"); n != 1 {
		t.Errorf("penanda \"Pasal 7\" muncul %d kali, mau 1:\n%s", n, isi)
	}
}

// TestPasalNomorTidakSahDitolak menjaga nomor dari query string tidak pernah
// diteruskan mentah-mentah.
func TestPasalNomorTidakSahDitolak(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("x"))

	for _, target := range []string{"/pasal", "/pasal?nomor=", "/pasal?nomor=abc", "/pasal?nomor=7x"} {
		rec := panggil(t, srv, target)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, mau %d", target, rec.Code, http.StatusBadRequest)
		}
	}
}

// TestPasalTidakDitemukanMembalasMarkup menjaga klik pada pasal yang tidak ada
// tetap menampilkan pesan, bukan wadah kosong.
//
// Balasannya 200, bukan 404: htmx tidak menukar respons non-2xx, sehingga
// status error justru membuat tombolnya terasa tidak bekerja.
func TestPasalTidakDitemukanMembalasMarkup(t *testing.T) {
	kosong := func(context.Context, string) ([]string, error) { return nil, nil }

	srv, err := New(alirkanTetap("x"), kosong, Tampilan{Title: "uji"}, nil)
	if err != nil {
		t.Fatalf("membuat server: %v", err)
	}

	rec := panggil(t, srv, "/pasal?nomor=999")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}
	if isi := rec.Body.String(); !strings.Contains(isi, "tidak ditemukan") {
		t.Errorf("tidak ada pesan pasal tidak ditemukan:\n%s", isi)
	}
}

// TestPasalGagalDibacaMembalasMarkup memastikan kegagalan Qdrant tidak
// menggagalkan halaman.
func TestPasalGagalDibacaMembalasMarkup(t *testing.T) {
	gagal := func(context.Context, string) ([]string, error) { return nil, errors.New("qdrant mati") }

	srv, err := New(alirkanTetap("x"), gagal, Tampilan{Title: "uji"}, nil)
	if err != nil {
		t.Fatalf("membuat server: %v", err)
	}

	rec := panggil(t, srv, "/pasal?nomor=7")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}
	if isi := rec.Body.String(); !strings.Contains(isi, "gagal dimuat") {
		t.Errorf("tidak ada pesan gagal dimuat:\n%s", isi)
	}
}

// TestPasalTanpaPembacaMembalasMarkup menjaga server yang tidak punya pembaca
// pasal — fitur detail dimatikan — tetap membalas markup.
func TestPasalTanpaPembacaMembalasMarkup(t *testing.T) {
	srv := newTestServer(t, alirkanTetap("x"))
	srv.bacaPasal = nil

	rec := panggil(t, srv, "/pasal?nomor=7")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, mau %d", rec.Code, http.StatusOK)
	}
	if isi := rec.Body.String(); !strings.Contains(isi, "tidak tersedia") {
		t.Errorf("tidak ada pesan fitur mati:\n%s", isi)
	}
}

// TestAlirMerenderMarkdown memastikan jawaban model yang berformat Markdown
// sampai ke browser sebagai HTML, bukan sebagai penanda mentah.
func TestAlirMerenderMarkdown(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("**Tempat:** kulkas\n\n* 2 - 3 hari\n")),
		"/tanya/alir?q=x")

	isi := rec.Body.String()

	if !strings.Contains(isi, "<strong>Tempat:</strong>") {
		t.Errorf("tebal tidak dirender:\n%s", isi)
	}
	if !strings.Contains(isi, "<li>2 - 3 hari</li>") {
		t.Errorf("daftar tidak dirender:\n%s", isi)
	}
}

// TestAlirMengescapePotongan menutup celah penyuntikan lewat teks jawaban.
//
// htmx menukar isi event sebagai HTML, jadi potongan mentah dari model harus
// di-escape di server. Tanpa itu, teks seperti "<b>" akan jadi tag sungguhan.
func TestAlirMengescapePotongan(t *testing.T) {
	rec := panggil(t, newTestServer(t, alirkanTetap("<b>tebal</b> & <script>")), "/tanya/alir?q=x")

	isi := rec.Body.String()

	if strings.Contains(isi, "<b>tebal</b>") {
		t.Errorf("potongan tidak ter-escape, tag mentah lolos:\n%s", isi)
	}
	if !strings.Contains(isi, "&lt;b&gt;tebal&lt;/b&gt;") {
		t.Errorf("potongan tidak muncul dalam bentuk ter-escape:\n%s", isi)
	}
}

// TestAlirTetapMenutupSaatGagal memastikan halaman tidak menggantung ketika
// penjawab berhenti di tengah jalan: penutup harus tetap dikirim.
func TestAlirTetapMenutupSaatGagal(t *testing.T) {
	alirkan := func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		if err := delta("awal "); err != nil {
			return nil, err
		}
		return nil, errors.New("kuota habis")
	}

	rec := panggil(t, newTestServer(t, alirkan), "/tanya/alir?q=x")

	isi := rec.Body.String()

	if !strings.Contains(isi, "event: selesai") {
		t.Errorf("tidak ada event selesai saat gagal:\n%s", isi)
	}
	if !strings.Contains(isi, "event: tutup") {
		t.Errorf("tidak ada event tutup saat gagal:\n%s", isi)
	}
	// Teks yang sempat mengalir tetap dipertahankan, ditambah pesan error.
	// Spasi di ujung dipangkas oleh perender Markdown, jadi hasilnya "<p>awal</p>".
	if !strings.Contains(isi, "<p>awal</p>") {
		t.Errorf("jawaban sebagian hilang:\n%s", isi)
	}
	if !strings.Contains(isi, "kuota habis") {
		t.Errorf("pesan error tidak disertakan:\n%s", isi)
	}
}

// TestSajikanMenungguAliranSelesaiSaatDibatalkan adalah uji regresi untuk
// penutupan yang rapi.
//
// http.Server.Shutdown hanya *memulai* penutupan, dan http.Server.Serve sudah
// mengembalikan ErrServerClosed saat itu juga. Sebelumnya sajikan ikut kembali
// pada titik itu, sehingga proses pemanggil keluar sementara handler SSE masih
// mengalir — jawaban yang sedang tampil terpotong di tengah.
//
// Tes ini menahan handler di tengah aliran, membatalkan context (seperti
// Ctrl-C), lalu memastikan sajikan belum kembali. Setelah handler dilepas,
// aliran harus selesai utuh.
func TestSajikanMenungguAliranSelesaiSaatDibatalkan(t *testing.T) {
	mulai := make(chan struct{})
	lepas := make(chan struct{})

	alirkan := func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		if err := delta("pertama "); err != nil {
			return nil, err
		}

		close(mulai)
		<-lepas

		return nil, delta("terakhir")
	}

	srv := newTestServer(t, alirkan)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("membuka listener: %v", err)
	}

	ctx, batalkan := context.WithCancel(context.Background())
	defer batalkan()

	errCh := make(chan error, 1)
	go func() { errCh <- sajikan(ctx, ln, srv.Handler()) }()

	// Jalankan satu permintaan SSE dan kumpulkan isinya di latar belakang.
	bodyCh := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/tanya/alir?q=x")
		if err != nil {
			bodyCh <- ""
			return
		}
		defer resp.Body.Close()

		b, _ := io.ReadAll(resp.Body)
		bodyCh <- string(b)
	}()

	// Tunggu sampai handler benar-benar sedang mengalir.
	<-mulai

	// Tiru Ctrl-C.
	batalkan()

	// sajikan tidak boleh kembali selagi aliran belum selesai.
	select {
	case err := <-errCh:
		t.Fatalf("sajikan kembali sebelum aliran selesai (err=%v); jawaban akan terpotong", err)
	case <-time.After(300 * time.Millisecond):
	}

	// Lepaskan handler; sekarang aliran boleh selesai.
	close(lepas)

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("sajikan mengembalikan error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sajikan tidak kembali setelah aliran selesai")
	}

	select {
	case isi := <-bodyCh:
		for _, wajib := range []string{"event: delta", "event: selesai", "event: tutup"} {
			if !strings.Contains(isi, wajib) {
				t.Errorf("aliran terpotong, tidak memuat %q:\n%s", wajib, isi)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("permintaan tidak selesai")
	}
}

// TestKelasAdaDiCSS menjaga agar setiap kelas Tailwind yang muncul di markup
// yang dirender server benar-benar punya aturan di app.css.
//
// Tailwind hanya memindai direktori templates (lihat input.css), jadi kelas
// yang ditulis di dalam string Go tidak pernah ikut ter-build dan markup-nya
// tampil tanpa gaya — kegagalan yang tidak memunculkan error apa pun, baik saat
// build CSS maupun saat menjalankan server. Uji ini menangkapnya.
//
// Halaman utama ikut diperiksa, bukan hanya pesan singkat: di sanalah kelas
// tombol dan tata letaknya paling sering berubah.
func TestKelasAdaDiCSS(t *testing.T) {
	css, err := aset.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("membaca app.css: %v", err)
	}

	srv := newTestServer(t, alirkanTetap("x"))

	markup := map[string]string{
		"halaman utama": panggil(t, srv, "/").Body.String(),
		"blok rujukan":  srv.renderSumber([]string{"Pasal 7", "Lampiran"}),
		"blok saran":    srv.renderSaran([]string{"Apa syaratnya?", "Berapa lama prosesnya?"}),
		"isi pasal":     srv.renderPasal([]string{"Pasal 7 isi uji."}),
		"pasal kosong":  srv.renderPasalKosong("Pasal 7 tidak ditemukan."),
	}
	for _, nama := range []string{"pesan-baru", "pesan-galat", "pesan-sibuk"} {
		markup[nama] = srv.renderPesan(nama, "teks uji")
	}

	for nama, isi := range markup {
		if isi == "" {
			t.Errorf("%s tidak menghasilkan markup", nama)
			continue
		}

		// Seluruh atribut class diperiksa, bukan hanya yang pertama: satu
		// fragmen bisa memuat banyak elemen berkelas.
		kumpulan, ok := semuaAtribut(isi, "class")
		if !ok {
			t.Errorf("%s tidak punya atribut class", nama)
			continue
		}

		for _, kelas := range kumpulan {
			for _, k := range strings.Fields(kelas) {
				// htmx menyuntikkan gaya indikatornya sendiri saat berjalan
				// (includeIndicatorStyles), jadi kelasnya memang tidak ada di
				// app.css.
				if k == "htmx-indicator" {
					continue
				}

				if !kelasAdaDiCSS(css, k) {
					t.Errorf("%s memakai kelas %q yang tidak ada di app.css; "+
						"kelas di luar direktori templates tidak ikut ter-build", nama, k)
				}
			}
		}
	}
}

// kelasAdaDiCSS melaporkan apakah sebuah kelas punya aturan di app.css.
//
// Dua bentuk dicari: selector kelas biasa — dengan ".", "/", dan ":" yang
// di-escape Tailwind — dan bentuk atribut [class~=...] yang dipakai plugin
// typography untuk not-prose.
func kelasAdaDiCSS(css []byte, kelas string) bool {
	if bytes.Contains(css, []byte("[class~="+kelas+"]")) {
		return true
	}

	var b strings.Builder
	b.WriteByte('.')

	for _, r := range kelas {
		switch r {
		case '.', '/', ':', '%', '#', ',', '(', ')', '!', '[', ']':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}

	return bytes.Contains(css, []byte(b.String()))
}

// TestIndexMenyetelCookieSesi memastikan riwayat percakapan punya penanda.
//
// Tanpa cookie, server tidak punya cara membedakan pengunjung yang satu dari
// yang lain, dan semua orang akan berbagi satu riwayat.
func TestIndexMenyetelCookieSesi(t *testing.T) {
	c := cookieSesiMuat(t, newTestServer(t, alirkanTetap("x")))

	if c.Value == "" {
		t.Error("nilai cookie sesi kosong")
	}
	if !c.HttpOnly {
		t.Error("cookie sesi tidak HttpOnly; skrip di halaman bisa membacanya")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, mau Lax", c.SameSite)
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, mau %q", c.Path, "/")
	}
}

// TestRiwayatMenumpukDalamSatuSesi adalah uji inti memori di sisi web:
// pertanyaan kedua pada sesi yang sama harus menerima giliran pertama.
func TestRiwayatMenumpukDalamSatuSesi(t *testing.T) {
	var tercatat [][]Pesan

	alirkan := func(_ context.Context, pertanyaan string, riwayat []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		tercatat = append(tercatat, riwayat)
		return nil, delta("jawaban untuk " + pertanyaan)
	}

	srv := newTestServer(t, alirkan)
	cookie := cookieSesiMuat(t, srv)

	panggilDenganCookie(t, srv, "/tanya/alir?q=pertama", cookie)
	panggilDenganCookie(t, srv, "/tanya/alir?q=kedua", cookie)

	if len(tercatat) != 2 {
		t.Fatalf("alirkan dipanggil %d kali, mau 2", len(tercatat))
	}

	// Giliran pertama harus benar-benar mulai dari kosong.
	if len(tercatat[0]) != 0 {
		t.Errorf("giliran pertama sudah membawa riwayat: %v", tercatat[0])
	}

	mau := []Pesan{
		{Peran: PeranUser, Isi: "pertama"},
		{Peran: PeranAssistant, Isi: "jawaban untuk pertama"},
	}
	if !reflect.DeepEqual(tercatat[1], mau) {
		t.Errorf("riwayat giliran kedua = %v, mau %v", tercatat[1], mau)
	}
}

// TestSesiBerbedaTidakBerbagiRiwayat memastikan riwayat tidak bocor antar
// pengunjung.
func TestSesiBerbedaTidakBerbagiRiwayat(t *testing.T) {
	var tercatat [][]Pesan

	alirkan := func(_ context.Context, _ string, riwayat []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		tercatat = append(tercatat, riwayat)
		return nil, delta("ok")
	}

	srv := newTestServer(t, alirkan)

	// Dua kali membuka halaman utama tanpa cookie: server menerbitkan sesi
	// baru untuk masing-masing, seperti dua peramban yang berbeda.
	tabA := cookieSesiMuat(t, srv)
	tabB := cookieSesiMuat(t, srv)

	if tabA.Value == tabB.Value {
		t.Fatalf("dua sesi mendapat ID yang sama: %q", tabA.Value)
	}

	panggilDenganCookie(t, srv, "/tanya/alir?q=milik+A", tabA)
	panggilDenganCookie(t, srv, "/tanya/alir?q=milik+B", tabB)
	panggilDenganCookie(t, srv, "/tanya/alir?q=lanjutan+A", tabA)

	if len(tercatat) != 3 {
		t.Fatalf("alirkan dipanggil %d kali, mau 3", len(tercatat))
	}
	if len(tercatat[1]) != 0 {
		t.Errorf("sesi B mewarisi riwayat sesi A: %v", tercatat[1])
	}

	mau := []Pesan{
		{Peran: PeranUser, Isi: "milik A"},
		{Peran: PeranAssistant, Isi: "ok"},
	}
	if !reflect.DeepEqual(tercatat[2], mau) {
		t.Errorf("riwayat lanjutan sesi A = %v, mau %v", tercatat[2], mau)
	}
}

// TestPercakapanBaruMengosongkanRiwayat menguji tombol "Percakapan baru".
func TestPercakapanBaruMengosongkanRiwayat(t *testing.T) {
	var tercatat [][]Pesan

	alirkan := func(_ context.Context, _ string, riwayat []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		tercatat = append(tercatat, riwayat)
		return nil, delta("ok")
	}

	srv := newTestServer(t, alirkan)
	cookie := cookieSesiMuat(t, srv)

	panggilDenganCookie(t, srv, "/tanya/alir?q=pertama", cookie)

	rec := postBaru(srv, cookie)

	if rec.Code != http.StatusOK {
		t.Fatalf("status POST /baru = %d, mau %d", rec.Code, http.StatusOK)
	}
	// Balasan wajib berisi markup: htmx tidak melakukan swap kalau responsnya
	// kosong, sehingga tombolnya akan terasa tidak bekerja.
	if strings.TrimSpace(rec.Body.String()) == "" {
		t.Error("balasan POST /baru kosong; htmx tidak akan melakukan swap")
	}

	// Penanda ini yang dipakai #log untuk menghapus pengumumannya begitu
	// pertanyaan berikutnya masuk. Tanpa penanda, pengumuman menetap selamanya.
	if !strings.Contains(rec.Body.String(), `id="pesan-baru"`) {
		t.Errorf("balasan POST /baru tidak memuat id=pesan-baru: %q", rec.Body.String())
	}

	panggilDenganCookie(t, srv, "/tanya/alir?q=sesudah+reset", cookie)

	if len(tercatat) != 2 {
		t.Fatalf("alirkan dipanggil %d kali, mau 2", len(tercatat))
	}
	if len(tercatat[1]) != 0 {
		t.Errorf("riwayat masih ada setelah percakapan baru: %v", tercatat[1])
	}
}

// TestGiliranGagalTidakDicatat memastikan percakapan yang berhenti di tengah
// jalan tidak meninggalkan jejak di memori.
//
// Pengguna tidak pernah melihat jawaban itu, jadi ia tidak akan menganggapnya
// ada — kalau tetap tercatat, pertanyaan berikutnya akan merujuk sesuatu yang
// tidak pernah tampil di layar.
func TestGiliranGagalTidakDicatat(t *testing.T) {
	var tercatat [][]Pesan

	alirkan := func(_ context.Context, _ string, riwayat []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		tercatat = append(tercatat, riwayat)

		if len(tercatat) == 1 {
			return nil, errors.New("kuota habis")
		}
		return nil, delta("ok")
	}

	srv := newTestServer(t, alirkan)
	cookie := cookieSesiMuat(t, srv)

	panggilDenganCookie(t, srv, "/tanya/alir?q=gagal", cookie)
	panggilDenganCookie(t, srv, "/tanya/alir?q=berikutnya", cookie)

	if len(tercatat) != 2 {
		t.Fatalf("alirkan dipanggil %d kali, mau 2", len(tercatat))
	}
	if len(tercatat[1]) != 0 {
		t.Errorf("giliran yang gagal ikut tercatat: %v", tercatat[1])
	}
}

// TestSesiSibukDitolak menjaga satu sesi dari dua permintaan sekaligus.
//
// Dua tab di peramban yang sama memakai cookie yang sama, jadi keduanya
// berbagi satu sesi. Tanpa penjaga, jawaban yang masih mengalir dan jawaban
// baru sama-sama menuliskan gilirannya ke riwayat yang sama, dan urutannya
// jadi campur.
func TestSesiSibukDitolak(t *testing.T) {
	mulai := make(chan struct{})
	lepas := make(chan struct{})

	// Hanya panggilan pertama yang menahan aliran. Kalau penjaga sibuk gagal
	// bekerja, permintaan kedua ikut sampai ke sini — kalau panggilan kedua
	// juga menunggu, test-nya menggantung sampai timeout alih-alih gagal
	// dengan pesan yang jelas.
	var n int32

	alirkan := func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		if err := delta("sedang menyusun"); err != nil {
			return nil, err
		}

		if atomic.AddInt32(&n, 1) == 1 {
			close(mulai)
			<-lepas
		}

		return nil, nil
	}

	srv := newTestServer(t, alirkan)
	cookie := cookieSesiMuat(t, srv)

	selesai := make(chan struct{})
	go func() {
		serve(srv, "/tanya/alir?q=pertama", cookie)
		close(selesai)
	}()

	<-mulai

	// Permintaan kedua dari sesi yang sama harus ditolak dengan pesan yang
	// bisa dibaca, bukan error mentah maupun diam-diam menunggu.
	isi := serve(srv, "/tanya/alir?q=kedua", cookie).Body.String()

	if !strings.Contains(isi, "Masih menyusun jawaban sebelumnya") {
		t.Errorf("permintaan kedua tidak ditolak dengan pesan yang jelas:\n%s", isi)
	}
	if !strings.Contains(isi, "event: tutup") {
		t.Errorf("penolakan tidak menutup koneksi, EventSource akan menyambung ulang:\n%s", isi)
	}

	close(lepas)
	<-selesai
}

// TestResetSaatMengalirTidakMenghidupkanRiwayat menjaga urutan reset terhadap
// jawaban yang masih berjalan.
//
// Tanpa penghitung generasi, jawaban yang sedang mengalir akan menuliskan
// gilirannya setelah reset — riwayat yang sudah dikosongkan hidup lagi, dan
// pertanyaan berikutnya diam-diam masih melihat percakapan yang sudah
// ditinggalkan.
func TestResetSaatMengalirTidakMenghidupkanRiwayat(t *testing.T) {
	var tercatat [][]Pesan

	mulai := make(chan struct{})
	lepas := make(chan struct{})

	alirkan := func(_ context.Context, _ string, riwayat []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		tercatat = append(tercatat, riwayat)

		if err := delta("jawaban"); err != nil {
			return nil, err
		}

		if len(tercatat) == 1 {
			close(mulai)
			<-lepas
		}
		return nil, nil
	}

	srv := newTestServer(t, alirkan)
	cookie := cookieSesiMuat(t, srv)

	selesai := make(chan struct{})
	go func() {
		serve(srv, "/tanya/alir?q=sebelum+reset", cookie)
		close(selesai)
	}()

	<-mulai

	// Reset ditekan selagi jawaban pertama masih mengalir.
	postBaru(srv, cookie)

	close(lepas)
	<-selesai

	// Pertanyaan berikutnya harus benar-benar mulai dari konteks kosong.
	panggilDenganCookie(t, srv, "/tanya/alir?q=sesudah+reset", cookie)

	if len(tercatat) != 2 {
		t.Fatalf("alirkan dipanggil %d kali, mau 2", len(tercatat))
	}
	if len(tercatat[1]) != 0 {
		t.Errorf("riwayat hidup lagi setelah reset di tengah aliran: %v", tercatat[1])
	}
}

// newTestServerKode merakit server dengan jalur rekomendasi kode terpasang.
//
// rekomendasi boleh nil; itu mewakili server yang tidak mengaktifkan fitur.
func newTestServerKode(t *testing.T, alirkan Alirkan, rekomendasi RekomendasiKode) *Server {
	t.Helper()

	srv, err := New(alirkan, bacaPasalTetap, Tampilan{Title: "uji"}, rekomendasi)
	if err != nil {
		t.Fatalf("membuat server: %v", err)
	}
	return srv
}

// TestKodeKlasifikasiMemakaiJalurRekomendasi mengunci bahwa KK#perihal# tidak
// masuk ke jalur tanya-jawab biasa, melainkan ke jalur rekomendasi kode.
//
// '#' di query string harus di-encode menjadi %23: kalau tidak, peramban
// menganggapnya awal fragmen URL dan perihalnya tidak pernah sampai ke server.
func TestKodeKlasifikasiMemakaiJalurRekomendasi(t *testing.T) {
	var (
		perihalDiterima  string
		alirkanDipanggil bool
	)

	alirkan := func(context.Context, string, []Pesan, func(string) error, func([]string) error) (SaluranSaran, error) {
		alirkanDipanggil = true
		return nil, nil
	}
	rekomendasi := func(_ context.Context, perihal string, delta func(string) error) error {
		perihalDiterima = perihal
		return delta("Kode terbaik: 090.1 — Cuti tahunan")
	}

	srv := newTestServerKode(t, alirkan, rekomendasi)

	rec := panggil(t, srv, "/tanya/alir?q=KK%23permohonan+cuti+tahunan%23")
	isi := rec.Body.String()

	if alirkanDipanggil {
		t.Error("jalur tanya-jawab biasa ikut dipanggil untuk permintaan KK#")
	}
	if perihalDiterima != "permohonan cuti tahunan" {
		t.Errorf("perihal diterima = %q, mau %q", perihalDiterima, "permohonan cuti tahunan")
	}
	if !strings.Contains(isi, "data: Kode terbaik: 090.1 — Cuti tahunan") {
		t.Errorf("tidak ada delta rekomendasi:\n%s", isi)
	}
	if !strings.Contains(isi, "event: selesai") || !strings.Contains(isi, "event: tutup") {
		t.Errorf("penutup aliran tidak lengkap:\n%s", isi)
	}
	if strings.Contains(isi, "event: sumber") || strings.Contains(isi, "event: saran") {
		t.Errorf("jalur kode tidak boleh mengirim sumber maupun saran:\n%s", isi)
	}
}

// TestKodeKlasifikasiTidakMasukRiwayat memastikan permintaan klasifikasi tidak
// meninggalkan jejak di riwayat sesi.
//
// Kalau tercatat, pertanyaan perbup berikutnya akan melihat konteks yang tidak
// relevan, dan jawabannya bisa melenceng tanpa sebab yang terlihat.
func TestKodeKlasifikasiTidakMasukRiwayat(t *testing.T) {
	var tercatat [][]Pesan

	alirkan := func(_ context.Context, _ string, riwayat []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		tercatat = append(tercatat, riwayat)
		return nil, delta("dijawab")
	}
	rekomendasi := func(_ context.Context, _ string, delta func(string) error) error {
		return delta("rekomendasi kode")
	}

	srv := newTestServerKode(t, alirkan, rekomendasi)
	cookie := cookieSesiMuat(t, srv)

	// Permintaan klasifikasi lebih dulu; ia tidak boleh meninggalkan jejak.
	panggilDenganCookie(t, srv, "/tanya/alir?q=KK%23perihal+pertama%23", cookie)

	// Pertanyaan biasa sesudahnya harus melihat riwayat kosong.
	panggilDenganCookie(t, srv, "/tanya/alir?q=pertanyaan+biasa", cookie)

	if len(tercatat) != 1 {
		t.Fatalf("jalur tanya-jawab biasa dipanggil %d kali, mau 1", len(tercatat))
	}
	if len(tercatat[0]) != 0 {
		t.Errorf("riwayat pertanyaan biasa = %v, mau kosong (KK# tidak boleh tercatat)", tercatat[0])
	}
}

// TestKodeKlasifikasiTanpaSeamJadiPertanyaanBiasa memastikan perilaku lama
// tidak berubah ketika fitur tidak dipasang: server dengan rekomendasi nil
// memperlakukan KK#perihal# sebagai pertanyaan biasa.
func TestKodeKlasifikasiTanpaSeamJadiPertanyaanBiasa(t *testing.T) {
	dipanggil := false

	alirkan := func(_ context.Context, _ string, _ []Pesan, delta func(string) error, _ func([]string) error) (SaluranSaran, error) {
		dipanggil = true
		return nil, delta("dijawab sebagai pertanyaan biasa")
	}

	srv := newTestServerKode(t, alirkan, nil)
	panggil(t, srv, "/tanya/alir?q=KK%23perihal%23")

	if !dipanggil {
		t.Error("jalur tanya-jawab biasa tidak dipanggil padahal rekomendasi nil")
	}
}

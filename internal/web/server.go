// Package web menyajikan antarmuka web untuk pipeline RAG yang sama dengan
// yang dipakai perintah CLI "ask": mencari potongan dokumen yang relevan di
// Qdrant, lalu meminta Gemini menyusun jawaban dari potongan itu.
//
// Bedanya, jawaban di sini dikirim bertahap lewat Server-Sent Events sehingga
// terlihat mengalir, bukan menunggu selesai lalu muncul sekaligus.
//
// Riwayat percakapan disimpan per sesi di memori proses, ditandai cookie
// rag_sesi, sehingga pertanyaan lanjutan bisa merujuk jawaban sebelumnya.
// Riwayat ini hilang saat server dimatikan; tidak ada penyimpanan permanen.
//
// Antarmuka dibangun dengan htmx (atribut di HTML, tanpa JavaScript yang
// ditulis sendiri) dan Tailwind CSS. Kedua berkas itu disajikan dari
// direktori static lewat go:embed, bukan dari CDN.
package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark"
)

// aset menyatukan template dan berkas statis ke dalam biner, sehingga program
// tidak bergantung pada direktori kerja saat dijalankan.
//
//go:embed templates static
var aset embed.FS

const (
	// namaCookieSesi adalah cookie yang menandai satu percakapan.
	namaCookieSesi = "rag_sesi"

	// sesiTTL adalah umur diam maksimum sebuah sesi. Sesi yang lebih lama dari
	// ini dibuang saat halaman utama dibuka, supaya riwayat pengunjung yang
	// sudah pergi tidak menumpuk di memori selamanya.
	sesiTTL = 30 * time.Minute
)

// Peran pengirim pesan di riwayat percakapan. Diekspor karena pemanggil di
// luar paket ini — cmd/rag — perlu menerjemahkannya ke peran Eino.
const (
	PeranUser      = "user"
	PeranAssistant = "assistant"
)

// Pesan adalah satu giliran percakapan yang disimpan untuk sebuah sesi.
//
// Tipe ini sengaja netral — hanya string, tanpa tipe Eino — supaya paket web
// tidak perlu tahu apa-apa tentang schema.Message. Konversi ke tipe Eino
// dikerjakan di cmd/rag, tempat chain-nya memang sudah dikenal.
type Pesan struct {
	Peran string // PeranUser atau PeranAssistant
	Isi   string
}

// Alirkan menjalankan satu pertanyaan dan menulis potongan jawaban lewat delta.
//
// riwayat berisi giliran-giliran sebelumnya untuk sesi ini, terlama lebih dulu.
// Isinya kosong pada giliran pertama atau ketika memori dimatikan. Pemanggil
// yang menentukan berapa banyak yang dipakai; paket web menyimpan riwayat
// seutuhnya supaya batas HISTORY_MESSAGES hanya ditentukan di satu tempat.
//
// delta dipanggil untuk setiap potongan. Kalau delta mengembalikan error —
// biasanya karena klien memutus koneksi — Alirkan sebaiknya berhenti dan
// mengembalikan error itu.
//
// sumber dipanggil sekali dengan label rujukan potongan yang dipakai — mis.
// "Pasal 7" — sebelum delta pertama. Boleh tidak dipanggil sama sekali kalau
// tidak ada yang bisa dirujuk. Error dari sumber diperlakukan sama seperti
// error dari delta.
//
// Jawaban utuh tidak dikembalikan karena handler sudah menyusunnya sendiri dari
// potongan yang benar-benar dikirim ke layar. Dengan begitu HTML final dijamin
// cocok dengan teks yang tadi terlihat mengalir.
//
// Tipe ini sengaja dipakai sebagai seam: paket web tidak perlu tahu apa-apa
// tentang Eino, Qdrant, atau Gemini, sehingga handler-nya bisa diuji tanpa
// jaringan maupun API key.
type Alirkan func(
	ctx context.Context,
	pertanyaan string,
	riwayat []Pesan,
	delta func(string) error,
	sumber func([]string) error,
) error

// Tampilan adalah keterangan yang dipajang di kepala halaman.
type Tampilan struct {
	// Title dan Subtitle diisi dari konfigurasi, bukan ditulis di template,
	// supaya mengganti dokumen sumber tidak memerlukan perubahan HTML.
	Title      string
	Subtitle   string
	Model      string
	Collection string
	TopK       int
}

// dataTanya adalah isi fragmen untuk satu pertanyaan.
type dataTanya struct {
	Pertanyaan string
	// TanyaURL adalah pertanyaan yang sudah di-encode untuk dipakai sebagai
	// query string koneksi SSE.
	TanyaURL string
}

// sesi menyimpan riwayat percakapan satu pengunjung.
//
// mu melindungi seluruh isinya. Kuncinya perlu karena satu sesi bisa disentuh
// dari dua goroutine sekaligus: aliran SSE yang sedang berjalan dan permintaan
// berikutnya dari tab yang sama.
type sesi struct {
	mu       sync.Mutex
	riwayat  []Pesan
	sibuk    bool
	generasi uint64
	terakhir time.Time
}

// mulai menandai sesi sedang melayani satu jawaban. Hasilnya false kalau sesi
// itu masih sibuk.
//
// Penjaga ini perlu karena dua tab di peramban yang sama memakai cookie yang
// sama, jadi keduanya berbagi satu sesi. Tanpa penjaga, jawaban yang masih
// mengalir di tab pertama dan jawaban baru di tab kedua akan sama-sama
// menuliskan gilirannya ke riwayat yang sama, dan urutannya jadi campur.
func (s *sesi) mulai() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sibuk {
		return false
	}
	s.sibuk = true
	s.terakhir = time.Now()
	return true
}

// selesai menandai jawaban sudah tidak berjalan lagi.
func (s *sesi) selesai() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sibuk = false
	s.terakhir = time.Now()
}

// salinRiwayat mengembalikan salinan riwayat beserta nomor generasi saat ini.
//
// Disalin, bukan dikembalikan langsung, supaya penambahan di akhir jawaban
// tidak mengubah slice yang sedang dibaca Alirkan.
func (s *sesi) salinRiwayat() ([]Pesan, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Pesan(nil), s.riwayat...), s.generasi
}

// catat menambahkan satu giliran ke riwayat, tapi hanya kalau generasinya belum
// berubah.
//
// Generasi naik ketika pengguna menekan "Percakapan baru". Jawaban yang masih
// mengalir saat tombol itu ditekan tidak boleh menuliskan riwayatnya ke sesi
// yang sudah dikosongkan — kalau tidak, konteks lama hidup lagi dan pertanyaan
// berikutnya diam-diam masih melihat percakapan yang sudah ditinggalkan.
func (s *sesi) catat(generasi uint64, pesan ...Pesan) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.generasi != generasi {
		return
	}
	s.riwayat = append(s.riwayat, pesan...)
	s.terakhir = time.Now()
}

// kosongkan menghapus riwayat dan menaikkan generasi.
func (s *sesi) kosongkan() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.riwayat = nil
	s.generasi++
	s.terakhir = time.Now()
}

// Server menyajikan halaman tanya-jawab.
type Server struct {
	alirkan  Alirkan
	tampilan Tampilan
	tmpl     *template.Template
	md       goldmark.Markdown
	statik   http.Handler

	// mu melindungi map sesi; isi tiap sesi dilindungi mu milik sesi itu.
	mu   sync.Mutex
	sesi map[string]*sesi
}

// New menyiapkan server dari fungsi penjawab dan keterangan tampilan.
func New(alirkan Alirkan, tampilan Tampilan) (*Server, error) {
	tmpl, err := template.ParseFS(aset, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("membaca template: %w", err)
	}

	statik, err := fs.Sub(aset, "static")
	if err != nil {
		return nil, fmt.Errorf("membaca aset statis: %w", err)
	}

	return &Server{
		alirkan:  alirkan,
		tampilan: tampilan,
		tmpl:     tmpl,
		// Tanpa opsi html.WithUnsafe, sehingga HTML mentah yang mungkin
		// ditulis model tidak bisa menyisipkan tag ke halaman.
		md:     goldmark.New(),
		statik: http.FileServerFS(statik),
		sesi:   make(map[string]*sesi),
	}, nil
}

// ambilSesi mengembalikan sesi milik permintaan, atau nil kalau permintaan itu
// tidak membawa cookie sesi yang dikenal. Pemanggil memperlakukan nil sebagai
// percakapan tanpa riwayat.
func (s *Server) ambilSesi(r *http.Request) *sesi {
	c, err := r.Cookie(namaCookieSesi)
	if err != nil || c.Value == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ss, ok := s.sesi[c.Value]
	if !ok {
		return nil
	}

	ss.mu.Lock()
	ss.terakhir = time.Now()
	ss.mu.Unlock()

	return ss
}

// sesiBaru membuat sesi kosong dan mengembalikan ID-nya.
func (s *Server) sesiBaru() (string, *sesi) {
	id := idSesi()
	ss := &sesi{terakhir: time.Now()}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.sesi[id] = ss
	return id, ss
}

// buangSesiKedaluwarsa membersihkan sesi yang sudah lama tidak dipakai.
//
// Dipanggil saat halaman utama dibuka, bukan dari goroutine latar: dengan cara
// ini tidak ada goroutine yang perlu dihentikan khusus ketika server ditutup.
func (s *Server) buangSesiKedaluwarsa() {
	batas := time.Now().Add(-sesiTTL)

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, ss := range s.sesi {
		ss.mu.Lock()
		// Sesi yang sedang melayani jawaban tidak dibuang, supaya aliran yang
		// masih berjalan tidak kehilangan riwayatnya di tengah jalan.
		kedaluwarsa := ss.terakhir.Before(batas) && !ss.sibuk
		ss.mu.Unlock()

		if kedaluwarsa {
			delete(s.sesi, id)
		}
	}
}

// idSesi membuat ID sesi acak.
//
// Acak dan panjang, bukan berurutan, supaya riwayat satu pengunjung tidak bisa
// dibaca pengunjung lain hanya dengan menebak nilai cookie-nya.
func idSesi() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand praktis tidak pernah gagal. Kalau gagal, aplikasi tetap
		// jalan dengan ID berbasis waktu alih-alih panic.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// Handler mengembalikan seluruh rute aplikasi.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// {$} membatasi pola ini hanya pada "/" persis, bukan semua path.
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /tanya", s.handleTanya)
	mux.HandleFunc("GET /tanya/alir", s.handleAlir)
	mux.HandleFunc("POST /baru", s.handleBaru)
	mux.Handle("GET /statik/", http.StripPrefix("/statik/", s.statik))

	return mux
}

// Serve menjalankan server HTTP sampai ctx dibatalkan, lalu menutupnya dengan
// rapi supaya permintaan yang sedang berjalan tidak terpotong.
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	// Alamat dari listener dipakai, bukan addr, supaya port ":0" tetap
	// dilaporkan sebagai port yang benar-benar dipakai.
	log.Printf("server web berjalan di http://%s", ln.Addr())

	return sajikan(ctx, ln, s.Handler())
}

// sajikan melayani permintaan dari ln sampai ctx dibatalkan.
//
// Fungsi ini tidak kembali sebelum penutupan benar-benar selesai. Itu
// penting: http.Server.Shutdown hanya *memulai* penutupan, dan Serve di
// bawah mengembalikan ErrServerClosed begitu penutupan dimulai — bukan
// setelah selesai. Tanpa menunggu di sini, program langsung keluar dan
// permintaan yang masih berjalan, misalnya jawaban yang sedang dialirkan
// lewat SSE, ikut terpotong di tengah jalan.
func sajikan(ctx context.Context, ln net.Listener, handler http.Handler) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Penutupan berjalan di goroutine sendiri karena Serve di bawah harus
	// tetap terpanggil. Selesainya dilaporkan lewat channel supaya pemanggil
	// bisa menunggu.
	selesai := make(chan struct{})
	go func() {
		defer close(selesai)

		<-ctx.Done()
		// Context terpisah karena ctx sudah dibatalkan; Shutdown tetap butuh
		// batas waktu supaya tidak menggantung selamanya. Batas ini juga
		// menentukan berapa lama Ctrl-C menunggu jawaban yang sedang mengalir
		// selesai dikirim.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	<-selesai
	return nil
}

// handleIndex menyajikan halaman utama.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.buangSesiKedaluwarsa()

	// Cookie sesi hanya disetel di sini, bukan di setiap permintaan.
	//
	// Kalau disetel juga di /tanya atau /tanya/alir, header Set-Cookie bisa
	// ikut terkirim di tengah aliran SSE — dan karena setiap respons membuat
	// ID baru, sesi yang sedang dipakai justru tertimpa dan riwayatnya hilang.
	if _, err := r.Cookie(namaCookieSesi); err != nil {
		id, _ := s.sesiBaru()
		http.SetCookie(w, &http.Cookie{
			Name:     namaCookieSesi,
			Value:    id,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := s.tmpl.ExecuteTemplate(w, "index.html", s.tampilan); err != nil {
		// Header sudah terkirim, jadi tidak bisa lagi membalas dengan status
		// error; cukup catat supaya tidak gagal diam-diam.
		log.Printf("web: merender halaman utama: %v", err)
	}
}

// handleTanya mengembalikan fragmen untuk satu pertanyaan: gelembung pertanyaan
// beserta elemen yang akan membuka koneksi SSE.
//
// Fragmen ini sengaja tidak berisi jawaban. htmx menyisipkannya ke halaman,
// dan begitu masuk ke DOM, ekstensi SSE membuka koneksi ke /tanya/alir.
func (s *Server) handleTanya(w http.ResponseWriter, r *http.Request) {
	pertanyaan := strings.TrimSpace(r.URL.Query().Get("q"))
	if pertanyaan == "" {
		http.Error(w, "pertanyaan kosong", http.StatusBadRequest)
		return
	}

	data := dataTanya{
		Pertanyaan: pertanyaan,
		// Pertanyaan ikut di URL karena EventSource hanya mendukung GET.
		TanyaURL: url.QueryEscape(pertanyaan),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if err := s.tmpl.ExecuteTemplate(w, "tanya.html", data); err != nil {
		log.Printf("web: merender fragmen pertanyaan: %v", err)
	}
}

// handleAlir mengalirkan jawaban sebagai Server-Sent Events.
//
// Urutan event yang dikirim:
//
//	delta  …   potongan teks mentah, ditambahkan ke layar
//	selesai    HTML final hasil render Markdown, menukar seluruh wadah jawaban
//	tutup      memicu sse-close di klien
//
// "tutup" dipisah dari "selesai" supaya penutupan koneksi tidak berlomba
// dengan penukaran HTML terakhir.
func (s *Server) handleAlir(w http.ResponseWriter, r *http.Request) {
	pertanyaan := strings.TrimSpace(r.URL.Query().Get("q"))
	if pertanyaan == "" {
		http.Error(w, "pertanyaan kosong", http.StatusBadRequest)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Supaya proxy di depan tidak menahan respons sampai selesai, yang akan
	// mematikan efek streaming-nya.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// ResponseController dipakai supaya tetap bekerja kalau ResponseWriter
	// dibungkus middleware (misalnya pencatat akses).
	rc := http.NewResponseController(w)

	flush := func() error {
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}

	kirim := func(nama, isi string) error {
		if err := tulisEvent(w, nama, isi); err != nil {
			return err
		}
		return flush()
	}

	_ = flush()

	ss := s.ambilSesi(r)
	if ss != nil {
		if !ss.mulai() {
			s.tolakSibuk(kirim)
			return
		}
		defer ss.selesai()
	}

	var riwayat []Pesan
	var generasi uint64
	if ss != nil {
		riwayat, generasi = ss.salinRiwayat()
	}

	// Jawaban dikumpulkan sambil dialirkan, karena HTML final perlu dirender
	// dari teks yang sudah utuh.
	var utuh strings.Builder

	// Rujukan dikirim sebagai event sendiri supaya tidak tercampur ke dalam
	// teks jawaban. Kalau ikut dialirkan lewat delta, ia akan masuk juga ke
	// riwayat percakapan dan ikut terbaca model pada giliran berikutnya —
	// padahal ia bukan bagian dari jawaban.
	kirimSumber := func(sumber []string) error {
		if len(sumber) == 0 {
			return nil
		}
		return kirim("sumber", s.renderSumber(sumber))
	}

	err := s.alirkan(r.Context(), pertanyaan, riwayat, func(potongan string) error {
		utuh.WriteString(potongan)
		// Potongan di-escape karena htmx menukar isi event sebagai HTML.
		// Tanpa ini, teks seperti "<b>" dari model akan disisipkan sebagai tag.
		return kirim("delta", html.EscapeString(potongan))
	}, kirimSumber)

	if err != nil {
		if r.Context().Err() != nil {
			// Klien memutus koneksi; tidak ada lagi yang bisa dikirimi.
			return
		}
		log.Printf("web: menjawab pertanyaan gagal: %v", err)
	}

	// Giliran dicatat hanya kalau jawabannya benar-benar selesai. Percakapan
	// yang gagal di tengah jalan tidak boleh jadi konteks pertanyaan
	// berikutnya — pengguna tidak pernah melihat jawabannya, jadi ia tidak
	// akan menganggapnya ada.
	if err == nil && ss != nil {
		ss.catat(generasi,
			Pesan{Peran: PeranUser, Isi: pertanyaan},
			Pesan{Peran: PeranAssistant, Isi: utuh.String()},
		)
	}

	final := s.renderMarkdown(utuh.String())
	if err != nil {
		// Pesan error disertakan supaya halaman tidak menggantung menunggu
		// jawaban yang tidak akan datang.
		final += s.renderPesan("pesan-galat", fmt.Sprintf("Terhenti: %v", err))
	}

	if err := kirim("selesai", final); err != nil {
		return
	}
	_ = kirim("tutup", "")
}

// tolakSibuk membalas permintaan yang datang saat sesi yang sama masih
// melayani jawaban sebelumnya.
//
// Balasannya tetap berupa aliran SSE, bukan status 4xx. EventSource tidak
// menampilkan isi respons non-2xx — ia hanya menyambung ulang, dan percobaan
// ulang itu justru menambah beban pada sesi yang memang sedang sibuk.
func (s *Server) tolakSibuk(kirim func(nama, isi string) error) {
	_ = kirim("selesai", s.renderPesan("pesan-sibuk",
		"Masih menyusun jawaban sebelumnya. Tunggu sampai selesai, lalu kirim pertanyaan lagi."))
	_ = kirim("tutup", "")
}

// handleBaru mengosongkan riwayat percakapan sesi ini, sehingga pertanyaan
// berikutnya tidak lagi melihat percakapan sebelumnya.
//
// Balasannya wajib berisi markup: htmx tidak melakukan swap kalau responsnya
// kosong, sehingga tombolnya akan terasa tidak bekerja.
func (s *Server) handleBaru(w http.ResponseWriter, r *http.Request) {
	if ss := s.ambilSesi(r); ss != nil {
		ss.kosongkan()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if _, err := io.WriteString(w, s.renderPesan("pesan-netral",
		"Percakapan baru dimulai. Pertanyaan berikutnya tidak memakai konteks sebelumnya.")); err != nil {
		log.Printf("web: membalas percakapan baru: %v", err)
	}
}

// renderPesan merender salah satu template pesan singkat.
//
// Pesannya diletakkan di template, bukan sebagai string di sini, karena
// Tailwind hanya memindai direktori templates: kelas yang ditulis di dalam
// string Go tidak ikut ter-build, dan pesannya akan tampil tanpa gaya.
func (s *Server) renderPesan(nama, isi string) string {
	var b strings.Builder

	if err := s.tmpl.ExecuteTemplate(&b, nama, isi); err != nil {
		log.Printf("web: merender pesan %s: %v", nama, err)
		return html.EscapeString(isi)
	}
	return strings.TrimSpace(b.String())
}

// renderMarkdown mengubah jawaban Markdown menjadi HTML.
func (s *Server) renderMarkdown(teks string) string {
	var b strings.Builder

	if err := s.md.Convert([]byte(teks), &b); err != nil {
		log.Printf("web: merender markdown: %v", err)
		return "<p>" + html.EscapeString(teks) + "</p>"
	}
	return b.String()
}

// renderSumber merender daftar rujukan potongan dokumen.
//
// Sama seperti renderPesan, markup-nya ada di template supaya kelas Tailwind-nya
// ikut ter-build.
func (s *Server) renderSumber(sumber []string) string {
	var b strings.Builder

	if err := s.tmpl.ExecuteTemplate(&b, "sumber", sumber); err != nil {
		log.Printf("web: merender sumber: %v", err)
		return ""
	}
	return strings.TrimSpace(b.String())
}

// tulisEvent menulis satu pesan Server-Sent Events.
//
// Setiap baris isi wajib punya awalan "data:" sendiri. Baris baru yang
// dibiarkan mentah akan dipotong oleh browser, sehingga sisa isinya hilang —
// dan HTML hasil render Markdown hampir selalu berisi baris baru.
func tulisEvent(w io.Writer, nama, isi string) error {
	if _, err := fmt.Fprintf(w, "event: %s\n", nama); err != nil {
		return err
	}

	for _, baris := range strings.Split(isi, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", baris); err != nil {
			return err
		}
	}

	_, err := io.WriteString(w, "\n")
	return err
}

# RAG dengan Gemini + Eino + Qdrant

Versi **Go** dari notebook pelatihan `Copy_of_RAG_Gemini_AVPN_IT_Data.ipynb`
(Python + LangChain + ChromaDB + Gemini).

Tujuannya sama: membangun **RAG (Retrieval-Augmented Generation)** yang bisa
menjawab pertanyaan berdasarkan isi sebuah dokumen — tanpa model perlu dilatih
ulang, cukup dokumennya diganti.

Dokumen yang dipakai sekarang: **Peraturan Bupati Blitar Nomor 18 Tahun 2025
tentang Tata Naskah Dinas di Lingkungan Pemerintah Kabupaten Blitar**. Dokumen
sumbernya bebas diganti; lihat [Mengganti dokumen](#mengganti-dokumen).

Bedanya dengan notebook:

| Notebook (Python) | Proyek ini (Go) |
|---|---|
| LangChain | CloudWeGo **Eino** |
| ChromaDB | **Qdrant** |
| Google Colab | CLI lokal + client web |

---

## Alur RAG

```
Dokumen sumber (PDF atau Markdown)
        │
        │  rag ingest  ← jalankan sekali
        ▼
  Loader + Parser          parser dipilih dari ekstensi berkas
        ▼
  Splitter                 potong pada batas Pasal (lihat "Chunking sadar-struktur")
        ▼
  Saringan degenerat       buang potongan terlalu pendek / nyaris tanpa huruf
        ▼
  Embedder                 ubah tiap chunk jadi vector 768 angka
        ▼
  Qdrant                   simpan vector + teks + metadata (pasal, bab, bagian)

  ─────────────────────────────────────────────────────

  Pertanyaan user (+ riwayat percakapan)
        │
        │  rag ask / rag serve  ← bisa dijalankan berulang
        ▼
  Condense                 tulis ulang pertanyaan lanjutan jadi pertanyaan mandiri
        ▼
  Retriever                cari 10 chunk paling mirip
        │                  + kalau pertanyaan menyebut "pasal N", ambil
        │                    seluruh potongan pasal itu dan taruh di depan
        │
        ├─────────────────► Gemini (kedua)   susun saran pertanyaan lanjutan
        │                                    ← hanya web, berjalan bersamaan
        ▼                                      dengan jawaban
  Prompt Template          gabungkan konteks + riwayat + pertanyaan
        ▼
  Gemini                   susun jawaban
        ▼
  Jawaban (+ tombol saran di web)
```

Jalur tulis (embedding) dan jalur baca (pencarian) dipisah, sama seperti
notebook. Embedding hanya dijalankan sekali; setelah itu `rag ask` bisa dipakai
berkali-kali tanpa memanggil API embedding lagi.

Satu tambahan yang tidak ada di notebook: **memori percakapan**. Lihat
[Memori percakapan](#memori-percakapan).

Diagram di atas menggambarkan **jalur cepat** — chain linear yang jadi bawaan.
Ada satu jalur baca alternatif: **mode agen** (`AGENT_MODE=true`), di mana model
memilih sendiri langkah pencariannya lewat tool. Jalur linear tetap dipakai
sebagai cadangan bila agen gagal. Lihat [Mode agen](#mode-agen-eksperimental).

---

## 1. Prasyarat

- Go 1.27 atau lebih baru
- Qdrant (lihat langkah 2)
- Gemini API key (lihat langkah 3)
- **xberg** — opsional, hanya untuk mengubah PDF menjadi Markdown (lihat
  [Mengganti dokumen](#mengganti-dokumen)). Tidak diperlukan kalau dokumen
  sumbernya sudah berupa `.md`/`.txt`. Program Go-nya sendiri tidak memanggil
  xberg; konversinya dijalankan manual sekali per dokumen.

## 2. Menjalankan Qdrant

Qdrant perlu jalan di latar belakang. Pilih salah satu cara berikut.

### Cara A — paket `.deb` (butuh sudo, paling rapi)

```bash
curl -sLO https://github.com/qdrant/qdrant/releases/latest/download/qdrant_1.19.1-1_amd64.deb
sudo dpkg -i qdrant_1.19.1-1_amd64.deb
sudo systemctl enable --now qdrant
```

### Cara B — binary langsung (tanpa sudo)

```bash
mkdir -p ~/qdrant && cd ~/qdrant
curl -sL https://github.com/qdrant/qdrant/releases/latest/download/qdrant-x86_64-unknown-linux-musl.tar.gz | tar xz
./qdrant
```

Biarkan terminal ini terbuka. Data Qdrant disimpan di `./storage`.

### Cek Qdrant sudah jalan

```bash
curl -s localhost:6333/healthz
# harus menjawab: healthz check passed
```

> **Penting soal port.** Qdrant membuka dua port: **6333 (REST)** dan
> **6334 (gRPC)**. Program Go ini memakai **6334**, karena Go client Qdrant
> berkomunikasi lewat gRPC. Port 6333 hanya dipakai untuk pengecekan manual
> dengan `curl`. Kalau tertukar, koneksi akan gagal.

## 3. Mengisi API key

```bash
cp .env.example .env
```

Ambil API key di <https://aistudio.google.com> → **Get API Key**, lalu isi
`GEMINI_API_KEY` di file `.env`. File `.env` sudah masuk `.gitignore` sehingga
tidak akan ikut ter-commit.

Kunci ini selalu dipakai untuk embedding. Kalau ingin menghemat kuota chat
dengan mengalihkan chat model ke proxy lokal, lihat
[Mode proxy](#mode-proxy-chat-model-opsional) — `GEMINI_API_KEY` tetap
diperlukan untuk embedding.

## 4. Menjalankan

```bash
# 1. Lihat dulu bagaimana teks dipotong (tidak butuh API key / Qdrant)
go run ./cmd/rag chunk-demo

# 2. Baca dokumen, potong, embed, simpan ke Qdrant — jalankan sekali
go run ./cmd/rag ingest

# 3. Tanya-jawab interaktif
go run ./cmd/rag ask

# 4. Kalau jawabannya melenceng: periksa apa yang sebenarnya terambil
go run ./cmd/rag cari "pasal 1 peraturan ini membahas apa"
```

Contoh sesi `ask`:

```
Model: gemini-3.5-flash-lite | Collection: perbup_18_2025 | top-k: 10
Ketik pertanyaan lalu Enter. Keluar dengan 'exit', Ctrl-D, atau Ctrl-C.
Riwayat 6 pesan terakhir dipakai untuk memahami rujukan. Ketik 'reset' untuk mengosongkannya.

> apa saja jenis naskah dinas yang diatur?
retriever mengembalikan 10 chunk

Berdasarkan konteks yang diberikan, jenis Naskah Dinas sebagaimana dimaksud
dalam Pasal 7 terdiri atas:

  • Naskah Dinas arahan;
  • Naskah Dinas korespondensi; dan
  • Naskah Dinas khusus.

Di mana Naskah Dinas arahan sendiri terdiri atas:
  • Naskah Dinas pengaturan;
  • Naskah Dinas penetapan; dan
  • Naskah Dinas penugasan.

> kalau yang poin pertama di atas itu apa?
retriever mengembalikan 10 chunk

Naskah Dinas pengaturan merupakan Naskah Dinas arahan yang bersifat mengatur…
```

Perhatikan jawaban pertama menyebut **Pasal 7** — nomor pasal ikut terbawa karena
Markdown hasil konversi xberg mempertahankan struktur dokumen aslinya.

Pertanyaan kedua sengaja tidak memuat kata kunci apa pun yang ada di dokumen:
"poin pertama di atas" hanya bisa dipahami dari percakapan sebelumnya. Di sinilah
memori percakapan bekerja — lihat [Memori percakapan](#memori-percakapan).

Untuk keluar dari prompt `ask`, ada tiga cara: ketik `exit` (atau `quit`) lalu
Enter, tekan **Ctrl-D**, atau tekan **Ctrl-C**. Ketik `reset` (atau `baru`)
untuk mengosongkan riwayat tanpa keluar.

### Memeriksa hasil pencarian (`rag cari`)

Jawaban yang salah tidak memberi tahu di mana masalahnya. Bisa jadi potongan
yang dibutuhkan tidak terambil sama sekali, bisa juga terambil tapi disimpulkan
keliru oleh model. Dari jawaban akhir keduanya terlihat sama.

`rag cari` menjalankan **retrieval saja** — tanpa menulis ulang pertanyaan dan
tanpa memanggil model — lalu menampilkan peringkat, skor, metadata, dan isi
tiap potongan yang diambil. Dengan begitu ketiga kemungkinan itu bisa
dibedakan, dan itu yang menentukan apakah yang perlu diperbaiki chunking,
`TOP_K`, atau promptnya.

```bash
go run ./cmd/rag cari "pasal 1 peraturan ini membahas apa"

# Ambil lebih banyak dari TOP_K, untuk melihat seberapa dalam potongan yang
# dicari sebenarnya berada
go run ./cmd/rag cari -topk 30 "pasal 1 peraturan ini membahas apa"

# Tampilkan isi potongan selengkapnya, bukan ringkasan 200 karakter
go run ./cmd/rag cari -penuh "pasal 1 peraturan ini membahas apa"
```

Pertanyaannya diambil dari argumen yang tersisa, jadi tidak perlu dikutip —
tapi mengutipnya membuat batas pertanyaan jelas.

Contoh keluaran:

```
Pertanyaan : pasal 1 peraturan ini membahas apa
Collection : perbup_18_2025 | top-k: 10

#1  skor 0.7168  [pasal]
    ayat=1-5 bab=I bab_judul=KETENTUAN UMUM bagian=batang_tubuh pasal=1 …
    Pasal 1 angka (1)-(5) Dalam Peraturan Bupati ini yang dimaksud dengan: …
#2  skor 0.7055  [pasal]
    ayat=8-10 bab=I bab_judul=KETENTUAN UMUM bagian=batang_tubuh pasal=1 …
#8  skor 0.7315  [vektor]
    ayat=1 bab=III bab_judul=JENIS, SUSUNAN DAN BENTUK NASKAH DINAS pasal=31 …
```

Penanda `[pasal]`, `[rujukan]`, dan `[vektor]` menunjukkan **mengapa** sebuah
potongan ikut terambil — lihat
[Rujukan pasal langsung](#rujukan-pasal-langsung).

### Format jawaban

Model menjawab dalam Markdown. Sebelum dicetak, jawabannya ditata oleh
`cmd/rag/markdown.go`: `**tebal**` menjadi tebal, `*` menjadi butir `•`, dan
judul menjadi tebal — sehingga penanda mentah tidak mengotori layar.

Penataan ini **otomatis mati kalau stdout bukan terminal**, misalnya saat
keluaran dialihkan ke file atau disalurkan lewat pipe. Jadi jawaban yang
disimpan tetap Markdown bersih tanpa kode ANSI:

```bash
go run ./cmd/rag ask < pertanyaan.txt > jawaban.md
```

Isi variabel `NO_COLOR` untuk mematikannya secara manual.

Perender ini sengaja sederhana dan bukan Markdown lengkap: hanya penanda yang
benar-benar muncul di jawaban RAG yang ditangani. Tabel tidak dirapikan, dan
garis bawah tunggal (`_miring_`) dibiarkan apa adanya supaya teks seperti
`snake_case` tidak ikut berubah.

Di bawah setiap jawaban, `rag ask` mencetak baris **Rujukan** berisi bagian
dokumen yang dipakai — "Pasal 7, Pasal 31, …". Isinya sama dengan yang
ditampilkan client web; lihat [Rujukan di bawah jawaban](#rujukan-di-bawah-jawaban).
Dicetak setelah jawaban, bukan sebelumnya: di terminal tidak ada tempat menaruh
rujukan di samping jawaban, dan mendahuluinya dengan daftar panjang justru
menunda yang dicari pengguna.

### Client web (Tailwind + htmx)

Selain CLI, ada antarmuka web yang menjawab pertanyaan yang sama dan menampilkan
jawabannya **bertahap** lewat Server-Sent Events.

```bash
# 1. Build CSS sekali saja (butuh Node). Hasilnya ikut di-commit.
npm install
npm run build:css

# 2. Jalankan servernya
go run ./cmd/rag serve
```

Lalu buka <http://127.0.0.1:8080>. Port dan alamatnya bisa diubah:

```bash
go run ./cmd/rag serve -addr 0.0.0.0:9000
```

Judul dan keterangan di kepala halaman diambil dari `APP_TITLE` dan
`APP_SUBTITLE` di `.env`, jadi mengganti topik tidak perlu menyentuh template.

Default-nya sengaja `127.0.0.1` supaya tidak terbuka ke jaringan. **Tidak ada
autentikasi** — jangan bind ke `0.0.0.0` di jaringan yang tidak dipercaya.

Menekan Ctrl-C tidak langsung mematikan server: jawaban yang sedang mengalir
ditunggu sampai selesai dikirim (maksimal 10 detik) supaya tidak terpotong di
tengah. Permintaan baru sudah tidak diterima sejak Ctrl-C ditekan.

**Penting: `npm run build:css` harus diulang setiap kali kelas Tailwind di
`internal/web/templates/` berubah.** Tailwind menyusun CSS dengan memindai kelas
yang benar-benar dipakai, dan hasilnya disajikan lewat `go:embed`. Kalau lupa,
kelas baru tidak akan punya gaya — gejalanya tampilan terlihat "setengah jadi"
tanpa pesan error apa pun. Selama mengembangkan tampilan, jalankan
`npm run watch:css` di terminal terpisah.

Pemindaian itu dibatasi ke direktori `templates/` (lihat `@source` di
`input.css`), jadi **kelas yang ditulis di dalam string Go tidak ikut
ter-build** — termasuk markup yang dibalas langsung oleh handler. Markup
seperti itu sebaiknya diletakkan di template; lihat `templates/pesan.html`
sebagai contoh. Test `TestKelasAdaDiCSS` menjaga hal ini untuk halaman utama,
pesan singkat, dan blok rujukan.

Bagaimana streaming-nya bekerja:

| Bagian | Berkas |
|---|---|
| Halaman dan fragmen | `internal/web/templates/` |
| Handler HTTP + SSE | `internal/web/server.go` |
| Tailwind sumber / hasil | `internal/web/static/input.css` → `app.css` |
| htmx + ekstensi SSE | `internal/web/static/*.js` (di-vendor lokal, bukan CDN) |

Server mengirim lima jenis event: `delta` (potongan teks mentah, ditambahkan ke
layar), `sumber` (daftar rujukan potongan, lihat di bawah), `selesai` (HTML
final hasil render Markdown yang menukar seluruh wadah jawaban), `saran`
(tombol pertanyaan lanjutan, lihat di bawah), dan `tutup` (menutup koneksi).
Event `tutup` sengaja dipisah: ekstensi SSE htmx punya penyambungan ulang
otomatis, jadi tanpa penutup eksplisit pertanyaan akan dijalankan ulang setelah
stream selesai. Urutannya `sumber → delta* → selesai → saran → tutup`; `saran`
baru siap setelah jawaban habis, jadi ia menyusul di antara `selesai` dan
`tutup`.

Kotak pertanyaan menempel di dasar layar: tinggi halaman dikunci ke tinggi layar
(`h-dvh`) dan hanya daftar jawaban yang menggulir. Jawaban yang sedang mengalir
ikut digulirkan otomatis lewat modifier `show:bottom` milik htmx — tanpa
JavaScript tambahan, karena menambah isi di ujung daftar tidak menggeser posisi
gulir dengan sendirinya.

Perhatikan juga bahwa teks dari model dan pertanyaan dari pengguna **di-escape**
sebelum dikirim, dan HTML hasil render Markdown diproses tanpa `html.WithUnsafe`
— jadi HTML mentah dari model tidak bisa menyisipkan tag ke halaman.

Di kepala halaman ada tombol **Percakapan baru** yang mengosongkan riwayat sesi —
sama seperti perintah `reset` di CLI.

#### Pertanyaan contoh

Di bawah kotak pertanyaan ada empat tombol pertanyaan siap-klik. Banyak
pengunjung tidak tahu dokumennya memuat apa, jadi pertanyaan pertama yang paling
sulit dijawab adalah "mau tanya apa" — tombol-tombol ini menjawabnya dengan
menunjukkan sendiri.

Tombolnya memakai `hx-vals` supaya pertanyaannya ditulis apa adanya, bukan
sebagai query string yang sudah di-encode: teksnya di situ sekaligus jadi label
tombolnya, dan keduanya harus sama. `type="button"` wajib ada — tanpa itu
peramban menganggapnya tombol kirim, formulir ikut terkirim, dan satu klik
menjalankan dua permintaan. Keduanya dijaga
`TestIndexMenyajikanPertanyaanContoh`.

Ganti daftar pertanyaannya di `internal/web/templates/index.html` setelah
mengganti dokumen; pertanyaan yang tidak terjawab dokumen justru menurunkan
kepercayaan pengunjung.

#### Rujukan di bawah jawaban

Berlaku di kedua antarmuka: `rag ask` mencetaknya sebagai baris teks, client web
sebagai deretan label di bawah jawaban.

Setiap jawaban disertai baris **Rujukan** berisi penanda bagian dokumen yang
dipakai menyusunnya — "Pasal 7", "Lampiran", atau "Pembukaan".

Alasannya: pembaca peraturan tidak cukup tahu jawabannya, ia perlu tahu pasal
mana yang harus dikutip. Tanpa itu, jawaban yang benar pun tidak bisa dipakai
untuk bekerja.

Rujukan diambil dari metadata potongan yang benar-benar terambil, **bukan dari
teks jawaban model**. Bedanya penting: model bisa keliru menyebut pasal, dan
rujukan yang dikarang model justru menyesatkan. Isi potongannya sendiri tidak
ditampilkan — itu akan mengulang jawaban di bawah jawaban.

Urutannya mengikuti peringkat retrieval, dan label yang sama hanya muncul
sekali: satu pasal sering terpecah menjadi beberapa potongan, dan menampilkan
"Pasal 89" tujuh kali tidak menambah informasi apa pun.

Perlu diketahui: ketika pertanyaan menyebut nomor pasal, daftar rujukannya bisa
menjadi panjang. Penyaringan per pasal menambahkan isi pasal yang diminta, tapi
pencarian vektor tetap mengembalikan `TOP_K` potongan di sampingnya — dan
semuanya benar-benar ikut menyusun konteks. Yang menjawab selalu ada di depan,
karena urutannya mengikuti peringkat.

Pasal hasil penelusuran rujukan silang juga ikut muncul di daftar ini. Untuk
pertanyaan *"Pasal 145 mengatur apa"*, rujukannya berbunyi **Pasal 145, Pasal
25** — dan itu memang bahan yang dipakai model, karena isi Pasal 145 menunjuk
ke Pasal 25. Kalau pembaca ingin tahu kenapa sebuah pasal yang tidak ia tanyakan
ada di daftar, urutannya menjelaskan: yang pertama diminta, yang berikutnya
dirujuk.

Aliran datanya:

```
retriever → rag.LabelSumber(docs) → rag.Pertanyaan.OnSumber
          → web.Alirkan(sumber)    → event SSE "sumber"
```

`OnSumber` sengaja ditaruh di `rag.Pertanyaan`, bukan dipanggil setelah aliran
habis: chain baru tahu potongan mana yang terpakai setelah node retrieval
berjalan, dan itu terjadi di tengah graph. Rujukan pun sampai ke layar sebelum
potongan jawaban pertama, jadi pembaca sudah tahu dasarnya sementara teksnya
masih mengalir.

Rujukan dikirim sebagai event tersendiri, bukan disisipkan ke teks jawaban,
supaya ia **tidak ikut masuk ke riwayat percakapan**. Kalau ikut, model akan
membacanya pada giliran berikutnya sebagai bagian dari jawabannya sendiri.

Batasan yang perlu diketahui:

- Pertanyaan ikut di query string koneksi SSE, karena `EventSource` hanya
  mendukung GET. Konsekuensinya pertanyaan tampil di URL dan riwayat browser.
- Riwayat percakapan disimpan **di memori proses**, bukan di disk. Mematikan
  server menghapus seluruh percakapan.
- Cookie sesi hanya menandai percakapan, bukan pengaman: **tidak ada
  autentikasi**, jadi siapa pun yang bisa menjangkau server ini bisa memulai
  percakapan baru.

#### Saran pertanyaan lanjutan

Di bawah blok rujukan, tiap jawaban di client web disertai beberapa tombol
pertanyaan lanjutan. Sama seperti pertanyaan contoh di halaman utama, banyak
pengunjung berhenti setelah satu jawaban karena tidak tahu apa lagi yang bisa
ditanyakan.

Tombolnya disusun model dari **pertanyaan pengguna dan konteks pasal yang
terambil** — bukan dari teks jawaban. Dua alasan:

- Konteks sudah ada di tangan saat retrieval selesai, jadi saran bisa mulai
  disusun **bersamaan dengan jawaban**, bukan setelahnya. Saat jawaban selesai
  dikirim, saran biasanya sudah siap.
- Membaca teks jawaban berarti menunggu jawaban selesai lebih dulu, dan
  pertanyaan lanjutan yang diturunkan dari jawaban cenderung mengulang jawaban
  itu alih-alih membuka arah baru.

Daftar pertanyaan yang **sudah diajukan** di percakapan itu ikut disertakan,
supaya tombol tidak menawarkan lagi hal yang sudah dijawab. Tanpa itu,
percakapan berlapis cenderung menampilkan saran yang sama berulang kali:
konteks dokumennya tidak berubah antar-giliran, jadi model pun mengusulkan hal
yang serupa. Sepuluh pertanyaan terakhir dikirim ke model; penyaringan hasilnya
memakai seluruh daftar, karena membandingkan teks jauh lebih murah daripada
mengirimkannya.

Kutipan dokumennya dipotong lebih dulu, pada batas potongan, supaya tidak
melebihi sekitar 4.000 karakter. Saran hanya butuh gambaran topik — dan di
[Mode agen](#mode-agen-eksperimental) konteks ini adalah gabungan bukti dari
semua tool, termasuk yang akhirnya tidak dipakai menjawab, sehingga bisa jauh
lebih panjang dari yang perlu.

Aliran datanya:

```
retriever → rag.LabelSumber(docs) → OnSumber  → event SSE "sumber"
          → rag.formatDocs(docs)  → OnKonteks → goroutine cmd/rag → rag.Saran
                                                   │ (bersamaan jawaban)
jawaban   → delta* → selesai ──────────────────────┤
                                                   ▼
                       handleAlir tunggu saluran → event SSE "saran" → "tutup"
```

`rag.Alirkan` mengembalikan **saluran** saran, bukan menerima callback. Callback
apa pun yang dipanggil di dalamnya pasti tiba sebelum event `selesai` — urutan
terbalik, karena `handleAlir` baru mengirim `selesai` setelah `alirkan` kembali.
Saluran juga memisahkan "jawaban selesai" dari "saran siap", sehingga jawaban
tidak ikut tertunda menunggu model saran.

Saran adalah **tambahan, bukan syarat**. Kegagalan apa pun di jalur ini — kuota
habis, model error, hasil kosong, atau terlalu lambat — tidak memengaruhi
jawaban, tidak menahan `tutup`, dan tidak menggagalkan handler:

| Keadaan | Yang terjadi |
|---|---|
| `SARAN_COUNT=0` | fitur mati; model kedua tidak pernah dipanggil, tidak ada event `saran` |
| Konteks kosong | model tidak dipanggil, saluran langsung ditutup |
| Model saran error | dicatat di log, jawaban tetap utuh dan koneksi tetap ditutup |
| Saran belum siap dalam 3 detik | penantian dilewati, `tutup` tetap dikirim, tombol tidak muncul |
| Jawaban gagal di tengah jalan | saran tidak ditunggu sama sekali — percakapan yang terhenti tidak pantas ditawari lanjutan |

Keluarannya satu pertanyaan per baris, bukan JSON: JSON yang rusak sedikit saja
akan mematikan seluruh daftar, sedangkan baris demi baris tetap bisa
diselamatkan. Penomoran, bullet, tanda kutip di ujung, baris kosong, baris
pembuka berakhiran titik dua, duplikat, pertanyaan pengguna sendiri, dan
pertanyaan yang sudah pernah ditanyakan dibuang sebelum ditampilkan.

Tombolnya mengirim pertanyaannya lewat **query string** (`hx-get="/tanya?q=…"`),
bukan `hx-vals` berisi JSON seperti tombol di halaman utama. Saran berasal dari
model dan bisa memuat tanda kutip: `hx-vals='{"q":"…"}'` akan rusak karena
html/template meng-escape kutip jadi `&#34;`, browser meng-decode-nya kembali
jadi kutip, dan JSON-nya jadi tidak sah — tombolnya tidak bereaksi. Pola query
string sudah dipakai koneksi SSE dan `handleTanya`.

Beban modelnya perlu diperhitungkan: tiap giliran di client web kini memakai
dua panggilan chat yang mulai hampir bersamaan. `SARAN_COUNT=0` mematikannya
tanpa mengubah apa pun yang lain.

### Menjalankan test

```bash
go test ./...
```

Test yang ada memverifikasi rangkaian node chain RAG tersambung dengan tipe
yang benar, perilaku streaming tetap bertahap (tidak ter-buffer), handler web
mengirim event SSE dengan urutan yang benar, dan penutupan server menunggu
jawaban yang sedang mengalir selesai sebelum keluar — semuanya tanpa
memerlukan API key maupun jaringan.

Rujukan juga diuji di dua lapis, tanpa API. Di `internal/rag`: bahwa nomor pasal
diutamakan sebagai label, bahwa potongan tanpa pasal jatuh ke nama bagiannya,
bahwa label kembar dibuang tanpa mengubah urutan peringkat, dan bahwa dokumen
tanpa metadata tetap mendapat label yang masuk akal. Di `internal/web`: bahwa
event `sumber` datang sebelum potongan jawaban pertama, bahwa ia tidak dikirim
sama sekali kalau tidak ada rujukan, bahwa labelnya di-escape, dan bahwa tombol
pertanyaan contoh benar-benar mengirim pertanyaannya.

Penelusuran rujukan silang diuji tanpa API maupun Qdrant, lewat retriever palsu
yang menjawab berbeda untuk tiap percobaan: bahwa rujukan diikuti dan pasal
hasil ikutan ditandai `[rujukan]`, bahwa nomor pasal sendiri tidak dianggap
rujukan, bahwa rujukan kembar dari dua pasal hanya diambil sekali, bahwa rujukan
yang juga muncul di hasil vektor tidak digandakan dan tetap ditandai `[rujukan]`,
bahwa batas jumlah ditegakkan, bahwa frasa tidak baku dan salah ketik tetap
tertangkap, serta bahwa penelusuran yang gagal atau kosong tidak pernah
menjatuhkan pencarian utama.

Splitter sadar-struktur juga diuji tanpa API: bahwa tiap penanda pasal dikenali
beserta judul BAB-nya, bahwa pasal pendek disimpan utuh tanpa header buatan,
bahwa pasal panjang dipecah pada batas ayat/butir tanpa kehilangan isi, bahwa
setiap potongan hasil pemecahan membawa rujukan pasalnya (pengunci bug yang
dilaporkan), bahwa penanda pasal palsu dan mundur ditolak, bahwa penanda setelah
Lampiran tidak dicap batang tubuh, bahwa dokumen tanpa penanda pasal jatuh ke
splitter cadangan, dan bahwa seluruh metadata bertipe string. Sebagian test itu
dijalankan terhadap **dokumen sungguhan**, bukan hanya teks buatan — termasuk
satu test yang sengaja gagal kalau tambalan Pasal 89-90 hilang (lihat bagian
"Mengganti dokumen").

Memori percakapan juga diuji tanpa API: bahwa pertanyaan lanjutan ditulis ulang
dan dipakai untuk retrieval sementara prompt tetap memakai pertanyaan asli,
bahwa penulisan ulang yang gagal (error **atau** hasil kosong) jatuh kembali ke
pertanyaan asli, bahwa giliran pertama tidak memanggil model ekstra, bahwa
riwayat menumpuk per sesi dan tidak bocor antar sesi, serta bahwa "Percakapan
baru" benar-benar mengosongkan riwayat — termasuk ketika ditekan selagi jawaban
masih mengalir.

Jalur saran juga diuji tanpa API, di tiga lapis. Di `internal/rag`: bahwa
keluaran model yang kotor (penomoran, bullet, tanda kutip, baris pembuka,
duplikat) dibersihkan, bahwa jumlahnya dibatasi, bahwa pertanyaan pengguna
sendiri tidak ditawarkan ulang, dan bahwa `SARAN_COUNT=0` serta konteks kosong
tidak memanggil model sama sekali. Di `cmd/rag`: bahwa saluran saran selalu
tertutup — termasuk saat konteks kosong, fitur mati, model gagal, dan context
dibatalkan — dan bahwa callback konteks yang terpanggil dua kali tetap hanya
memanggil model sekali. Di `internal/web`: bahwa urutan event
`selesai < saran < tutup` dijaga, bahwa tidak ada event `saran` kalau tidak ada
saran atau jawabannya gagal, bahwa koneksi tetap ditutup saat model saran
lambat (batas waktunya dipercepat di test), bahwa teks saran di-escape, dan
bahwa pertanyaan yang memuat tanda kutip bolak-balik utuh lewat query string
sampai ke `handleTanya`.

Satu test — `TestIDHasilGenerateCocokDenganYangTersimpan` — memang membaca
Qdrant, karena yang dibuktikan adalah ingest bersifat idempotent: ID yang
dihasilkan ulang dari dokumen sumber dicocokkan dengan ID yang sudah tersimpan.
Test ini **melewati dirinya sendiri** kalau Qdrant tidak jalan, collection masih
kosong, atau collection-nya dibangun dengan parameter pemotongan yang berbeda
(terlihat dari metadata `sidik_jari`) — dan tidak memanggil API embedding sama
sekali.

---

## Memori percakapan

Tanpa memori, setiap pertanyaan berdiri sendiri. Pertanyaan seperti *"kalau yang
poin pertama di atas itu apa?"* tidak akan terjawab, karena tidak ada satu pun
katanya yang bisa dicocokkan dengan isi dokumen.

Ada dua masalah berbeda yang harus diselesaikan, dan keduanya perlu:

1. **Retrieval butuh kata kunci.** Retriever mencari berdasarkan kemiripan
   vektor, jadi pertanyaan yang menggantung tidak menemukan apa pun.
2. **Model butuh riwayat.** Setelah konteks yang tepat ditemukan, model tetap
   perlu tahu apa yang dimaksud "poin pertama di atas".

Karena itu, sebelum retrieval, pertanyaan lanjutan ditulis ulang dulu menjadi
pertanyaan mandiri:

```
"kalau yang poin pertama di atas itu apa?"
        │
        │  condense  ← memakai riwayat percakapan
        ▼
"Apa itu Naskah Dinas pengaturan?"
        │
        │  retriever mencari dengan pertanyaan ini
        ▼
   chunk relevan  ──┐
                    │
   riwayat + pertanyaan ASLI pengguna ──┐
                    │                   │
                    ▼                   ▼
              prompt template ──► Gemini ──► jawaban
```

Perhatikan pembagiannya: **retrieval memakai pertanyaan hasil penulisan ulang,
tetapi model tetap menerima pertanyaan asli pengguna.** Pengguna harus melihat
jawaban atas apa yang benar-benar ia tanyakan, bukan atas pertanyaan yang
disusun ulang oleh mesin.

Kalau penulisan ulang gagal — kuota habis, jaringan putus, atau model
mengembalikan teks kosong — pertanyaan asli dipakai apa adanya. Jawaban jadi
kurang tepat, tapi tidak pernah gagal total. Giliran pertama juga tidak
memanggil model ekstra sama sekali, jadi perilaku tanpa riwayat persis sama
seperti sebelum fitur ini ada.

### Mengatur

| Variabel | Default | Keterangan |
|---|---|---|
| `HISTORY_MESSAGES` | `6` | jumlah pesan riwayat yang disertakan; `0` mematikan memori |

`6` berarti 3 giliran tanya-jawab terakhir — cukup untuk merujuk "poin a di
atas" tanpa membengkakkan prompt yang dikirim ke Gemini. Riwayat dipotong dari
**depan**, sehingga giliran terbaru selalu utuh.

Mengubah nilainya langsung terasa pada pertanyaan berikutnya, tanpa perlu
memulai ulang percakapan. `HISTORY_MESSAGES=0` mengembalikan perilaku lama:
setiap pertanyaan dijawab lepas.

### Memulai percakapan baru

- **CLI** — ketik `reset` (atau `baru`) lalu Enter.
- **Web** — tekan tombol **Percakapan baru** di kepala halaman.

Keduanya mengosongkan riwayat. Di web, jawaban lama juga hilang dari layar
karena seluruh isi daftar ditukar dengan pesan konfirmasi.

### Batasannya

- **Tidak ada penyimpanan permanen.** Riwayat hidup di memori proses; mematikan
  server atau menutup CLI menghapusnya. Percakapan tidak bisa dilanjutkan besok.
- **Satu sesi web = satu cookie.** Dua tab di peramban yang sama berbagi satu
  riwayat. Kalau tab kedua bertanya selagi tab pertama masih dijawab, permintaan
  kedua ditolak dengan pesan supaya urutan riwayatnya tidak campur.
- **Ini memori percakapan, bukan penyimpanan data aplikasi.** Untuk hal seperti
  keranjang belanja yang harus tetap ada besok dan tidak boleh salah hitung,
  riwayat chat bukan tempatnya — itu butuh penyimpanan tersendiri di sisi
  aplikasi.

---

## Chunking sadar-struktur

### Masalah yang memicunya

Pertanyaan **"pasal 1 peraturan ini membahas apa"** dijawab *"tidak terdapat
informasi mengenai Pasal 1"* — padahal Pasal 1 ada di dokumen, lengkap dengan
17 definisi. Penyebabnya bukan modelnya, melainkan cara dokumen dipotong.

Pemisah default `recursive` splitter adalah `["\n", ".", "?", "!"]`. Ia
memotong pada batas **kalimat**, tanpa menghiraukan struktur dokumen. Pasal 1
(2.858 karakter, 17 definisi) tercabik menjadi 8 potongan, dan **hanya potongan
pertama yang masih memuat tulisan "Pasal 1"**. Tujuh sisanya tidak bisa
ditemukan lewat rujukan pasal, berapa pun `TOP_K`-nya.

Terukur: potongan yang memuat "Pasal 1" berada di **peringkat 11 dari 259**
sementara `TOP_K=10`. Jadi ia memang tidak pernah sampai ke model.

### Kenapa per-Pasal, bukan per-ayat

Per-ayat justru terlalu **halus**, bukan terlalu panjang:

| Ukuran | Nilai |
|---|---|
| Jumlah ayat di dokumen | 236 |
| Median panjang satu ayat | **161 karakter** |
| Ayat di bawah 150 karakter | **42%** |
| Pasal yang tidak punya ayat sama sekali | **66 dari 153** |

Ayat sependek itu menghasilkan vektor yang nyaris tidak bermakna, dan 60 pasal
tidak akan punya unit sama sekali. Ayat juga saling merujuk — *"sebagaimana
dimaksud pada ayat (1)"* — sehingga berdiri sendiri menjadi kalimat yang
menggantung.

Karena itu unitnya adalah **pasal**. Ayat tetap dipakai, tapi hanya sebagai
titik potong ketika satu pasal tidak muat.

### Cara satu pasal dipecah

1. Dokumen dibagi pada batas **Pasal**, **BAB**, dan **LAMPIRAN**. Penanda
   pasal boleh menyambung dengan isinya di baris yang sama — lihat
   [Penanda pasal yang menyambung dengan isinya](#penanda-pasal-yang-menyambung-dengan-isinya).
2. Pasal yang muat (≤ `CHUNK_SIZE`) **disimpan utuh**, tanpa header buatan.
   Ini berlaku untuk sekitar 70% pasal, dan merupakan bentuk terbaik: isinya
   lengkap apa adanya.
3. Pasal yang lebih panjang dipecah pada batas **ayat** (`- (1) …`). Kalau
   pasalnya tidak berayat — seperti Pasal 1 yang memakai daftar definisi
   bernomor — batasnya **butir** (`- 5\. …`).
4. Kalau tidak ada batas internal sama sekali, dipotong berdasarkan ukuran.
5. **Setiap potongan hasil pemecahan diberi header rujukan.** Ini bagian yang
   memperbaiki bug di atas: tanpa header, potongan kedua dan seterusnya
   kehilangan tulisan "Pasal N".

Header yang dihasilkan mengikuti bentuk potongannya:

| Bentuk header | Kapan |
|---|---|
| `Pasal 1 angka (6)-(7)` | pasal tanpa ayat, dipecah per butir |
| `Pasal 89 ayat (1)-(3)` | pasal berayat, dipecah per ayat |
| `Pasal 89 ayat (1), bagian 2/2` | satu ayat sendirian lebih panjang dari `CHUNK_SIZE` |
| `Pasal 90 (bagian 1/2)` | tidak ada batas internal, dipecah berdasarkan ukuran |

Hasilnya untuk Pasal 1: **7 potongan, semuanya membawa rujukan pasalnya**, dan
seluruh 17 definisi tetap ada. Ini dikunci oleh
`TestSetiapChunkBatangTubuhDiawaliPasal` dan `TestDefinisiPasal1Lengkap` di
`internal/ingest/pasal_test.go`.

### Saringan potongan degenerat

Sebelum disimpan, potongan yang tidak berguna dibuang (`saringDegenerat`):

- lebih pendek dari **80 karakter**, atau
- punya kurang dari **40 karakter alfanumerik**, atau
- isinya kembar dengan potongan lain (setelah di-trim).

Ambang kedua itu yang menangkap bentuk paling berbahaya: deretan titik-titik
sebagai tempat isian pada template kop surat. Panjangnya melewati ambang
karakter, tapi vektornya nyaris degenerate — dan vektor seperti itu bisa
kebetulan berskor tinggi terhadap pertanyaan apa pun. Terukur: potongan
template itu berada di **peringkat 2** hasil pencarian "pasal 1", di atas
pasal-pasal sungguhan. Potongan seperti itu hanya memakan slot `TOP_K`.

### Penanda pasal yang menyambung dengan isinya

Konversi PDF tidak selalu memberi baris sendiri untuk penanda pasal. Di dokumen
ini ada lima yang ditulis menyambung:

```
Pasal 7 Jenis Naskah Dinas sebagaimana dimaksud dalam Pasal 4 huruf a terdiri atas:
Pasal 9 Jenis Naskah Dinas pengaturan terdiri atas:
Pasal 27 Naskah Dinas khusus terdiri dari:
Pasal 76 Dalam pembuatan Naskah Dinas sebagaimana dimaksud dalam Pasal 73 memuat unsur sebagai berikut:
Pasal 119 Ralat Naskah Dinas merupakan perbaikan yang dilakukan terhadap sebagian materi Naskah Dinas…
```

Pola yang menuntut barisnya persis `Pasal N` melewatkan kelimanya, sehingga
isinya menempel ke pasal sebelumnya dan nomor pasalnya menguap. Yang paling
merugikan adalah **Pasal 7** — isinya pengelompokan naskah dinas tingkat atas,
sehingga pertanyaan *"sebutkan pengelompokan naskah dinas dari level paling
atas"* tidak bisa dijawab.

Polanya karena itu menerima penanda yang menyambung, tetapi baris penanda hanya
dibuang kalau isinya **persis** penanda. Kalau tidak, kalimat pengantar pasalnya
— *"Jenis Naskah Dinas … terdiri atas:"* — ikut terbuang bersama barisnya.

### Penanda pasal palsu

Longgarnya pola di atas menuntut penjagaan di sisi lain, karena konversi PDF
juga bisa melempar potongan kalimat ke awal baris sehingga tampak seperti
penanda pasal. Dua bentuknya ada di dokumen ini:

| Baris | Sebenarnya |
|---|---|
| `Pasal 8` (di tengah Pasal 89) | potongan tabel yang rusak, isinya kata acak |
| `Pasal 112 Huruf (a) digunakan untuk …` | sambungan kalimat Pasal 113 ayat (1) yang terpotong baris |

Nomor pasal di batang tubuh selalu naik, jadi penanda yang nomornya tidak lebih
besar dari pasal terakhir ditolak, dan teksnya ikut ke pasal sebelumnya. Kedua
contoh di atas tertolak dengan cara yang sama: 8 lebih kecil dari 89, dan 112
lebih kecil dari 113.

Celah nomor tetap diterima: dokumen ini memang melompat dari 149 ke 150 tanpa
beberapa nomor.

### Lampiran

Lampiran tetap diindeks, tapi diberi tanda `bagian: "lampiran"` supaya bisa
disaring. Isinya tidak dibuang karena Lampiran memuat format-format yang
dirujuk pasal-pasal sebelumnya — misalnya format kop surat yang diminta
pertanyaan *"bagaimana format kop surat dinas"*.

Penanda pasal yang muncul setelah awal Lampiran tetap dicap `lampiran`, bukan
`batang_tubuh`: Lampiran boleh punya skema penomoran sendiri.

### Metadata setiap potongan

| Kunci | Isi | Contoh |
|---|---|---|
| `bagian` | letak potongan | `pembukaan`, `batang_tubuh`, `lampiran`, `tanpa_pasal` |
| `pasal` | nomor pasal | `1`, `89` |
| `ayat` | rentang ayat/butir di potongan ini | `1-5`, `17` |
| `bab` | nomor BAB | `I`, `IV` |
| `bab_judul` | judul BAB | `KETENTUAN UMUM` |
| `sidik_jari` | parameter pemotongan yang dipakai | `pasal/500/100` |

Semua nilainya **wajib bertipe string**. Indexer Qdrant memanggil
`qdrant.NewValueMap`, yang memanggil `NewValue`, dan `NewValue` **panic** —
bukan mengembalikan error — untuk tipe yang tidak dikenal. Metadata bertipe
liar akan menjatuhkan proses ingest, bukan melaporkan kesalahan yang bisa
ditelusuri. Dikunci oleh `TestMetadataSemuaBertipeString`.

`bab_judul` disimpan terpisah karena judul BAB adalah konteks yang tidak ada di
dalam isi pasal mana pun — tanpa itu ia hilang sama sekali dari hasil retrieval.

### Mengubah strategi

`CHUNK_STRATEGY=recursive` mengembalikan cara notebook (potong berdasarkan
ukuran saja), untuk dokumen yang tidak berstruktur pasal. Dokumen tanpa satu
pun penanda pasal **otomatis** jatuh ke jalur ini, jadi mengganti `DOC_PATH` ke
berkas non-peraturan tidak gagal — potongannya hanya diberi tanda
`bagian: "tanpa_pasal"`.

ID potongan diturunkan dari **isinya**, jadi mengubah `CHUNK_STRATEGY` atau
`CHUNK_SIZE` mengubah semua ID. Data lama di Qdrant tidak lagi tertimpa,
sehingga **collection harus dihapus lalu di-ingest ulang**:

```bash
curl -X DELETE localhost:6333/collections/$QDRANT_COLLECTION
go run ./cmd/rag ingest
```

Supaya keadaan ini tidak tertukar dengan bug idempotensi, setiap potongan
diberi metadata `sidik_jari`. Test idempotensi membacanya lebih dulu dan
melewati pengujian dengan pesan yang jelas kalau collection masih dibangun oleh
parameter pemotongan yang lama.

---

## Rujukan pasal langsung

Chunking sadar-struktur membuat pasal bisa **ditemukan**; bagian ini membuatnya
**terambil seluruhnya**.

Pencarian vektor menilai kemiripan makna. Pertanyaan *"pasal 1 peraturan ini
membahas apa"* bukan pertanyaan tentang topik, melainkan tentang **rujukan** —
dan semua potongan yang memuat kata "Pasal" berskor hampir sama. Setelah
perbaikan chunking, potongan yang memuat definisi 1-5 naik ke peringkat 9 dan
jawabannya benar, tetapi tetap hanya memuat **5 dari 17 definisi**.

Terukur: Pasal 1 terpecah menjadi 7 potongan, dan peringkat ketujuhnya di
pencarian vektor adalah:

| Potongan | Peringkat |
|---|---|
| definisi 1-5 | 9 |
| definisi 8-10 | 16 |
| definisi 17 | 40 |
| definisi 6-7 | **108** |
| definisi 13-14 | **139** |
| definisi 11-12 | **154** |
| definisi 15-16 | **167** |

Hanya **3 di antaranya masuk 100 besar**. Empat sisanya tidak terjangkau
berapa pun `TOP_K` yang masuk akal.

Menaikkan `TOP_K` bukan jawabannya. Pada `TOP_K=100` keempat potongan itu tetap
tidak muncul, sementara 100 potongan membanjiri prompt dengan konteks yang
tidak relevan.

Jadi begitu pertanyaan menyebut nomor pasal, potongan pasal itu diambil
**langsung** lewat filter metadata, tanpa bergantung pada kedekatan vektor
(`internal/rag/retriever.go`):

1. Pertanyaan dicocokkan dengan `(?i)\bpasal\s+(\d+)`.
2. Kalau ada rujukan, Qdrant disaring dengan `metadata.pasal` = nomor tersebut.
3. Pasal yang terambil dipindai untuk rujukan silangnya, lalu pasal yang
   dirujuk ikut diambil — satu lompatan saja (lihat bagian berikutnya).
4. Hasilnya diletakkan **di depan** hasil vektor, lalu digabung tanpa
   penggandaan.

Beberapa nomor digabung dengan **ATAU**, jadi *"apa bedanya pasal 8 dan pasal
19"* mengambil keduanya.

Hasil akhirnya untuk pertanyaan yang memulai semua ini: **seluruh 17 definisi
Pasal 1 ikut ke dalam konteks**, dan jawabannya menyebut ke-17-nya.

### Mengikuti rujukan silang satu lompatan

Pasal di peraturan ini saling menunjuk. Terukur pada dokumen contoh: **49 dari
155 pasal (32%)** memuat rujukan ke pasal lain, dan rujukan itu bukan hiasan.

| Pertanyaan | Sebelum | Sesudah |
|---|---|---|
| Pasal 145 mengatur apa | P145 | P145 + **P25** |
| Pasal 33 mengatur apa | P33 | P33 + **P27** |
| Pasal 89 ayat (2) berbunyi apa | P89 | P89 + **P87** |
| Pasal 28 mengatur apa | P28 | P28 + **P27** |
| Pasal 70 mengatur apa | P70 | P70 + **P27** |

Pasal 145 menyebut *"sebagaimana dimaksud dalam Pasal 25"*, dan Pasal 25 tidak
ikut terambil — pencarian vektor tidak punya alasan untuk mendekatkannya. Model
lalu menjawab tanpa bahan yang justru dirujuk. Pasal 33 lebih tajam lagi: ia
menyebut *"Surat kuasa sebagaimana dimaksud Pasal 27 huruf b"*, sementara Pasal
27 adalah tempat daftar jenis naskah dinas berada.

Empat hal yang membatasi langkah ini:

| Batas | Alasan |
|---|---|
| **Satu lompatan** — rujukan dari pasal hasil ikutan tidak diikuti lagi | Setiap lompatan menambah satu perjalanan ke Qdrant dan memperbesar konteks. Di dokumen contoh rujukan berantai tidak muncul: setiap pasal merujuk paling banyak satu pasal lain |
| **Maksimal 4 pasal** (`maksRujukanSilang`) | Batas ini tidak pernah tercapai di dokumen contoh, tetapi dipasang untuk dokumen lain yang rujukannya lebih rapat |
| **Hanya memindai potongan pasal**, bukan hasil vektor | Potongan hasil vektor adalah pecahan dari mana saja. Terukur: *"Apa itu lembar disposisi?"* ikut mengambil potongan Pasal 57 yang menyebut *"Pasal 27 huruf n"* — pemindaian hasil vektor akan menambahkan seluruh enumerasi jenis naskah dinas ke pertanyaan yang jawabannya sudah utuh di Pasal 24 dan 19 |
| **Nomor pasal sendiri dibuang** | Setiap potongan memuat nomornya di header — `Pasal 89 ayat (1)-(2)` — sehingga tanpa penyaringan ini setiap pasal dianggap merujuk dirinya sendiri |

Polanya sengaja selebar `\bpasal\s+(\d+)`, bukan mengunci frasa baku. Frasa
`sebagaimana dimaksud dalam Pasal` menangkap 45 rujukan; pola longgar 49. Empat
sisanya memakai frasa lain, dan satu di antaranya salah ketik di dokumen sumber:

```markdown
Berita daerah sebagiamana dimaksudd alam Pasal 27 huruf u …
```

Rujukan itu tetap ikut ditelusuri, dan itulah yang membuat baris terakhir tabel
di atas bekerja.

Kalau penelusuran rujukan gagal — Qdrant menolak, atau pasal yang dirujuk tidak
ada di collection — hasil pasal dan hasil vektor tetap dipakai apa adanya.
Rujukan silang adalah tambahan, bukan syarat.

### Bekerja sama dengan memori percakapan

Penyaringan ini diletakkan **setelah** langkah condense, dan itu membuat
keduanya saling melengkapi.

Pertanyaan lanjutan seperti *"yang nomor 10 di atas itu apa"* tidak memuat
rujukan apa pun. Tetapi condense lebih dulu menulis ulangnya menjadi pertanyaan
mandiri — *"Apa arti dari nomor 10 dalam daftar Pasal 1 peraturan tersebut?"* —
dan **rujukan pasalnya muncul dari situ**. Jadi pertanyaan lanjutan tentang
satu definisi tertentu pun ikut mendapat seluruh isi Pasal 1, bukan hanya
potongan yang kebetulan mirip.

Kalau penyaringan diletakkan sebelum condense, pertanyaan lanjutan seperti itu
tidak akan pernah tersaring.

### Kalau tidak ada yang disaring

Penyaringan ini **tidak mengubah apa pun** untuk pertanyaan yang tidak menyebut
nomor pasal — pertanyaan biasa tetap murni pencarian vektor. Tiga keadaan lain
juga jatuh kembali ke hasil vektor, bukan gagal:

| Keadaan | Perilaku |
|---|---|
| Pertanyaan tidak menyebut nomor pasal | hasil vektor saja, tanpa penandaan |
| Nomor pasal tidak ada di dokumen | hasil vektor saja, tanpa penandaan |
| Penyaringan ditolak Qdrant | hasil vektor saja, alasannya dicatat di log |
| Rujukan silang tidak menghasilkan potongan | hasil pasal + hasil vektor, alasannya dicatat di log |
| Penelusuran rujukan ditolak Qdrant | hasil pasal + hasil vektor, alasannya dicatat di log |

Kegagalan penyaringan sengaja tidak menjatuhkan seluruh pencarian: hasil vektor
sudah cukup untuk menjawab banyak pertanyaan, dan kehilangan konteks pasal
lebih baik daripada tidak menjawab sama sekali.

### Membaca penandanya di `rag cari`

Setiap hasil diberi penanda asalnya:

```
#1  skor 0.7168  [pasal]
    ayat=1-5 bab=I bab_judul=KETENTUAN UMUM bagian=batang_tubuh pasal=1 …
#5  skor 0.4211  [rujukan]
    ayat=1-3 bab=III bab_judul=JENIS, SUSUNAN DAN BENTUK NASKAH DINAS pasal=25 …
#8  skor 0.7315  [vektor]
    ayat=1 bab=III bab_judul=JENIS, SUSUNAN DAN BENTUK NASKAH DINAS pasal=31 …
```

`[pasal]` berarti potongan itu ada di situ **karena nomor pasalnya disebut**,
bukan karena mirip secara vektor. Skornya yang lebih rendah dari potongan
`[vektor]` di bawahnya bukan tanda keliru — memang begitu keadaannya, dan itu
sebabnya ia tidak akan pernah terambil tanpa penyaringan ini.

`[rujukan]` berarti potongan itu ikut karena ada pasal di atasnya yang
menunjuknya. Skornya paling rendah dari ketiganya, dan itu wajar: kemiripannya
dengan pertanyaan tidak relevan, yang membuatnya relevan adalah rujukan
silangnya. Kalau `[rujukan]` muncul padahal pertanyaan tidak menyebut pasal
apa pun, berarti ada yang salah — penelusuran rujukan hanya dipicu dari
potongan `[pasal]`.

---

## Mode agen (eksperimental)

Jalur baca bawaan adalah **chain linear**: satu kali penulisan ulang pertanyaan,
satu kali retrieval, satu kali model menjawab. Jumlah langkahnya tetap dan
hasilnya bisa diulang persis.

`AGENT_MODE=true` menggantinya dengan **agen ReAct terbatas**: model diberi
beberapa tool pencarian dan memutuskan sendiri mana yang dipanggil, berapa kali,
sampai ia merasa cukup untuk menjawab. Jalur linear tidak dihapus — ia tetap
dirakit dan dipakai sebagai cadangan.

### Cara mengaktifkan

```
AGENT_MODE=true
```

Berlaku untuk `rag ask` maupun `rag serve`. Saat aktif, `rag ask` mencetak baris
penanda; `rag serve` tidak berubah tampilannya.

### Tiga tool

| Tool | Argumen | Dipakai untuk |
|---|---|---|
| `cari_vektor` | `query` | pertanyaan bertopik yang tidak menyebut nomor pasal |
| `baca_pasal` | `nomor` | mengambil seluruh isi satu pasal yang disebut |
| `ikuti_rujukan` | `nomor` | menelusuri pasal yang dirujuk silang oleh suatu pasal |

Ketiganya mengembalikan potongan beserta label pasalnya, dan potongan yang
terkumpul dari **semua** tool digabung untuk menyusun rujukan di bawah jawaban
serta saran pertanyaan lanjutan. Callback-nya tetap menyala sekali, sama seperti
di jalur linear.

Setiap pemanggilan tool dicatat ke log, sehingga jejak agen bisa dibaca sama
seperti jalur linear:

```
rag: tool baca_pasal nomor=145 → 5 potongan
rag: tool ikuti_rujukan nomor=145 → 7 potongan [Pasal 145 Pasal 25]
```

### Cadangan ke jalur linear

Agen adalah tambahan, bukan syarat — prinsip yang sama dengan saran pertanyaan
lanjutan. Bila agen gagal **sebelum** mengeluarkan potongan jawaban pertama
(kuota habis, batas langkah terlampaui, jawaban kosong), permintaan otomatis
dijawab ulang lewat chain linear. Kegagalan setelah teks mulai terkirim tidak
bisa ditarik lagi, jadi diteruskan apa adanya.

### Apa yang berubah dibanding jalur linear

- **Tanpa `condense`.** Riwayat diserahkan apa adanya; agen yang merumuskan
  query toolnya sendiri. Perilaku giliran lanjutan karena itu bisa berbeda.
- **Tanpa penyaringan pasal otomatis.** `cari_vektor` sengaja murni kemiripan
  makna. Filter per-pasal dan penelusuran rujukan yang dulu otomatis kini jadi
  keputusan model lewat `baca_pasal` dan `ikuti_rujukan`. Ini memang tujuan tool
  halus, tetapi berarti kualitas bergantung pada pilihan model.
- **Biaya dan latensi naik.** Satu pertanyaan bisa berarti beberapa panggilan
  model dan beberapa kali retrieval, bukan satu.

### Pengaman

`AGENT_MAX_STEP` membatasi jumlah langkah; satu panggilan model atau satu
pemanggilan tool dihitung satu langkah. Bila habis, Eino mengembalikan error
`exceeds max steps` dan permintaan jatuh ke jalur linear. `AGENT_TIMEOUT_SECONDS`
adalah jaring pengaman kedua untuk satu permintaan yang menggantung.

Satu batasan yang perlu diketahui: pemeriksa tool-call bawaan Eino berhenti pada
potongan pertama yang berisi teks. Karena itu instruksi agen melarang menulis
kalimat pembuka sebelum memanggil tool — model yang menulis basa-basi lebih dulu
akan mengakhiri putaran tanpa pernah memanggil tool sama sekali.

---

## Mode proxy (chat model opsional)

Chat model bisa dialihkan dari API resmi Google ke endpoint apa pun yang
kompatibel OpenAI, misalnya [gemini-web2api](../gemini-web2api) — proxy yang
membungkus antarmuka web Gemini sehingga tidak memerlukan kuota API Google.

```bash
CHAT_PROVIDER=openai
CHAT_BASE_URL=http://localhost:8081/v1
CHAT_API_KEY=sk-gemini
GEMINI_CHAT_MODEL=gemini-3.6-flash   # nama model versi proxy, bukan API Google
```

Jalankan proxy-nya lebih dulu (dari folder `gemini-web2api`):

```bash
python3 gemini_web2api.py
curl -H "Authorization: Bearer sk-gemini" http://localhost:8081/v1/models
```

### Embedding tidak ikut beralih

`CHAT_PROVIDER` hanya mengganti **chat model**. Embedder tetap lewat API resmi
Google, karena antarmuka web Gemini tidak menyediakan endpoint embedding
(`POST /v1/embeddings` di proxy menjawab 404). Jadi `GEMINI_API_KEY` **tetap
wajib** — tujuannya menghemat kuota chat, bukan melepas ketergantungan pada
Google sepenuhnya.

Untuk benar-benar lepas dari `GEMINI_API_KEY`, embedder harus diganti ke
penyedia lain (mis. Ollama lokal), dan itu berarti **re-index seluruh
collection**: vector dari model berbeda tidak bisa dibandingkan, jadi data lama
harus dibuang dan `rag ingest` dijalankan ulang.

### Yang perlu diwaspadai

- **Tool calling lebih rapuh.** Proxy ini bukan memakai function calling asli,
  melainkan menyuruh model menulis blok ```` ```tool_call ```` lalu mengurainya
  dengan regex. Di [mode agen](#mode-agen-eksperimental) ini titik paling rawan:
  agen bergantung pada tool-call yang terstruktur, dan model kadang menjawab
  langsung tanpa memanggil tool atau mengeluarkan pesan sampah dari antarmuka
  web. Jalur chain linear lebih aman karena tidak memerlukan tool sama sekali.
- **Streaming + tool dikirim sebagai satu potongan.** Proxy harus mengurai
  jawaban penuh dulu untuk menemukan tool-call, jadi waktu tunggu potongan
  pertama di mode agen bisa berubah.
- **Tiap permintaan berdiri sendiri.** Multi-turn disimulasikan dengan
  menempelkan pesan sebelumnya ke prompt, sehingga token yang terpakai lebih
  banyak daripada API resmi.
- **Bergantung pada `gemini.google.com` dan bisa terkena rate limit.** Google
  dapat membatasi permintaan berfrekuensi tinggi.
- **Bukan API resmi.** Proxy ini hasil rekayasa balik antarmuka web, jadi bisa
  rusak sewaktu-waktu tanpa pemberitahuan.

Kalau `CHAT_PROVIDER=openai` tetapi proxy belum berjalan, `rag ingest` tetap
normal (tidak menyentuh chat model); yang gagal hanya `rag ask` dan `rag serve`.

---

## Padanan notebook → Go

| Sel notebook | Padanan di proyek ini |
|---|---|
| Instalasi library | `go.mod` |
| Konfigurasi API key | `internal/config` |
| `!curl -o ai_pv.pdf <url>` | `internal/ingest` → `ensureDoc` |
| Load Dokumen PDF | `internal/ingest` → `loadDoc` (parser dipilih dari ekstensi) |
| Chunking (`NLTKTextSplitter` / `RecursiveCharacterTextSplitter`) | `internal/ingest` → `splitDocs`; `pasal.go` untuk batas Pasal, `recursive` sebagai cadangan |
| Chat model | `internal/gemini` → `ChatModel` (backend dipilih lewat `CHAT_PROVIDER`) |
| Embedding | `internal/gemini` → `DocEmbedder` |
| Vector Database (`Chroma.from_documents`) | `internal/ingest` → `indexChunks` |
| Membuat Retriever | `internal/rag` → `New` |
| — (tambahan) | `internal/rag` → `retrieverPasal` (ambil langsung pasal yang disebut) |
| — (tambahan) | `internal/rag` → `Cari` (retrieval saja, untuk diagnosis) |
| Prompt Template | `internal/rag` → `systemInstruction` + `userTemplate` |
| RAG Chain (LCEL `\|`) | `internal/rag` → `compose.Graph` |
| Output Parser | `internal/rag` → `parseStream` |
| `input()` tanya-jawab | `cmd/rag` → `runAsk` |
| — (tambahan) | `internal/rag` → `condenseQuestion` (memori percakapan) |
| — (tambahan) | `internal/web` → client web dengan streaming SSE |

### RAG chain: dari LCEL ke Eino Graph

Di notebook, chain dirakit dengan pipe:

```python
rag_chain = (
    {"context": retriever | format_docs, "question": RunnablePassthrough()}
    | chat_template
    | chat_model
    | output_parser
)
```

Di Eino, rangkaian yang sama ditulis sebagai graph:

```
START → condense → prepare → prompt → model → parse → END
```

- `condense` = penulisan ulang pertanyaan lanjutan memakai riwayat percakapan.
  Ini **tambahan** yang tidak ada di notebook; lihat
  [Memori percakapan](#memori-percakapan).
- `prepare` = `{"context": retriever | format_docs, "question": RunnablePassthrough()}`
  — menerima pertanyaan, mengambil chunk relevan, menggabungkannya jadi konteks
- `prompt` = `chat_template`
- `model` = `chat_model`
- `parse` = `output_parser`

Menulisnya sebagai graph membuat setiap tahap bisa dilihat dan diganti satu per
satu, bukan tersembunyi di dalam rantai pipe. Menambah memori pun cukup
menyisipkan satu node di depan, tanpa mengubah node yang sudah ada.

Satu detail yang mudah salah: `condense` memakai `InvokableLambda`, sedangkan
`parse` wajib `TransformableLambda`. `InvokableLambda` menurunkan versi
stream-nya dengan mengumpulkan seluruh aliran lebih dulu, jadi memakainya di
`parse` akan mematikan streaming token. Di `condense` justru sebaliknya yang
diinginkan — hasil penulisan ulang harus utuh sebelum dipakai mencari.

---

## Catatan penting

Beberapa hal ini tidak kelihatan dari contoh di dokumentasi resmi, tapi bisa
membuat program gagal kalau diabaikan:

**1. Port Qdrant adalah 6334, bukan 6333.**
README komponen indexer Qdrant di `eino-ext` menulis `Port: 6333`. Itu keliru
untuk Go client — `qdrant.NewClient` berkomunikasi lewat gRPC di port 6334.

**2. Nama package `qdrant` bertabrakan.**
`github.com/qdrant/go-client/qdrant` dan
`github.com/cloudwego/eino-ext/components/{indexer,retriever}/qdrant` sama-sama
bernama `qdrant`. Contoh di README `eino-ext` mengimpor keduanya tanpa alias,
sehingga tidak akan compile. Di proyek ini keduanya diberi alias:
`qdrantclient`, `qdrantindexer`, `qdrantretriever`.

**3. Setiap chunk wajib punya ID, dan ID-nya dibuat deterministik.**
Parser dokumen tidak mengisi ID dokumen, sementara indexer Qdrant memakai
`doc.ID` sebagai point ID — dan Qdrant hanya menerima UUID atau bilangan bulat.

`internal/ingest` menurunkan ID setiap chunk dari **isi** chunk itu
(`assignChunkIDs`), bukan dari bilangan acak. Karena itu `ingest` bersifat
**idempotent**: menjalankan ulang dokumen yang sama akan menimpa titik yang sudah
ada, bukan menggandakannya. Efek sampingnya, dua chunk dengan isi identik
dianggap satu titik — untuk sumber pengetahuan RAG ini justru diinginkan, karena
teks kembar tidak menambah informasi.

Manfaat kedua dari ID deterministik: `ingest` jadi **inkremental**. Sebelum
meng-embed, ia membaca ID yang sudah ada di Qdrant (`existingChunkIDs`) lalu
melewati chunk yang sudah tersimpan (`filterNewChunks`). Jadi menjalankan ulang
setelah gagal di tengah hanya memproses sisa yang belum masuk — bukan mengulang
seluruh dokumen. Ini penting karena kuota embedding dihitung per teks.

**4. Embedder dokumen dan query berbeda.**
Gemini membedakan `RETRIEVAL_DOCUMENT` (untuk isi dokumen) dan `RETRIEVAL_QUERY`
(untuk pertanyaan). Notebook memakai satu embedder untuk keduanya; proyek ini
memisahkannya agar hasil pencarian lebih akurat.

**5. Eino tidak punya splitter NLTK.**
Notebook memakai `NLTKTextSplitter` (berbasis kalimat) di pendekatan pertama dan
`RecursiveCharacterTextSplitter` (berbasis karakter) di pendekatan kedua. Eino
menyediakan `recursive`, `semantic`, `markdown`, dan `html`. Proyek ini memakai
`recursive` sebagai **cadangan**, dan menambahkan splitter sadar-struktur
sendiri di `internal/ingest/pasal.go` untuk batang tubuh peraturan — lihat
[Chunking sadar-struktur](#chunking-sadar-struktur).

**6. `qdrant.Value` bukan `structpb.Value`.**
Client Qdrant memakai tipe `Value` buatan sendiri: ia mengikuti
`google.golang.org/protobuf/types/known/structpb`, tapi memisahkan
`IntegerValue` dan `DoubleValue` dari satu `NumberValue`. Karena itu ia tidak
punya `AsInterface`, dan `doc.MetaData["metadata"]` bertipe
`map[string]*qdrant.Value`, bukan `map[string]*structpb.Value`.

Mudah tertukar, dan salah menebak di sini gagalnya **diam-diam**: type
assertion gagal, map-nya nil, dan seluruh metadata hilang dari keluaran tanpa
satu pun error. Konversinya ditulis manual di `internal/rag/cari.go`
(`nilaiKeString`).

**7. Alamat metadata di payload Qdrant berawalan `metadata.`**
Indexer Qdrant mengumpulkan `doc.MetaData` di bawah satu kunci bernama
`metadata`, bukan sebagai kunci tingkat atas. Jadi nomor pasal beralamat
`metadata.pasal`, **bukan** `pasal`.

Salah alamat di sini gagalnya juga diam-diam: Qdrant tidak mengembalikan error,
hanya nol hasil. Filter yang tidak menyaring apa pun terlihat persis sama
dengan filter yang tidak menemukan apa pun. Dikunci oleh
`TestBidangPasalMemakaiAlamatPayload`.

**8. Bagian "Contoh Lain" di notebook tidak punya padanan.**
Bagian itu membandingkan `load_qa_chain` (gaya lama LangChain) dengan LCEL. Eino
menyeragamkan semuanya lewat `compose`, jadi tidak ada dua gaya yang perlu
dibandingkan.

**9. Nama model Gemini berubah sewaktu-waktu.**
Notebook memakai `gemini-2.5-flash-lite` (chat) dan `gemini-embedding-2`
(embedding). Saat proyek ini diuji, `gemini-2.5-flash-lite` sudah ditolak API
dengan pesan *"no longer available to new users"*, sehingga default di sini
memakai `gemini-3.5-flash-lite` — model yang direkomendasikan Google di pesan
error itu sendiri. `gemini-embedding-2` masih bekerja normal.

Kalau muncul error serupa, cek model apa saja yang masih tersedia untuk API
key-mu:

```bash
curl "https://generativelanguage.googleapis.com/v1beta/models?key=$GEMINI_API_KEY" \
  | grep -o '"models/[^"]*"'
```

Lalu ganti `GEMINI_CHAT_MODEL` atau `GEMINI_EMBED_MODEL` di `.env` — kode tidak
perlu diubah.

**10. Kuota embedding free tier: 100 teks per menit DAN 1000 teks per hari.**
Dokumen sebesar PERBUP ini (134 halaman → 309 chunk) tidak akan selesai dalam
satu menit. Tanpa penanganan khusus, `ingest` gagal di tengah jalan dengan
error `429 RESOURCE_EXHAUSTED` setelah sebagian data terlanjur tersimpan.

`internal/gemini/retry.go` membedakan dua batas itu:

- **Batas per menit** (100 teks) — menunggu lalu mengulang memang menolong. Ia
  membaca saran jeda dari pesan error Gemini (`"Please retry in 11.5s"`),
  menunggu, lalu mengulang.
- **Batas per hari** (1000 teks) — menunggu tidak menolong sama sekali. Ini
  dikenali dari `quotaId` yang memuat `PerDay`, dan `ingest` langsung berhenti
  dengan pesan yang menjelaskan bahwa batas ini baru pulih keesokan harinya
  (tengah malam Waktu Pasifik, sekitar 14:00 WIB).

Yang perlu diketahui: **kuota dihitung per teks, bukan per panggilan HTTP.**
Karena itu memperbesar `BatchSize` tidak menolong — menggabungkan teks ke dalam
batch yang lebih besar tidak mengurangi hitungannya. Satu dokumen 309 chunk
memakai 309 dari jatah 1000 teks harian.

Yang juga perlu diketahui: **kuota dihitung per model** (perhatikan akhiran
`PerModel-FreeTier` di `quotaId`). Jadi `gemini-embedding-2` yang jatahnya sudah
habis tidak memblokir `gemini-embedding-001` — masing-masing punya jatah
sendiri. Berguna kalau perlu menyelesaikan ingest hari itu juga. Tapi ingat:
**vector dari model berbeda tidak bisa dibandingkan**, jadi model pengganti
harus dipakai untuk meng-embed seluruh collection ke collection baru, bukan
untuk menanyakan collection lama. Verifikasi daftar model yang tersedia dengan:

```bash
curl -s "https://generativelanguage.googleapis.com/v1beta/models?key=$GEMINI_API_KEY" \
  | grep -o '"name": "models/[^"]*"'
```

Kalau `ingest` gagal di tengah jalan, cukup jalankan lagi: ID chunk yang
deterministik membuat `ingest` melewati titik yang sudah tersimpan, sehingga
hanya sisa chunk yang perlu di-embed.

**11. `MessagesPlaceholder` menolak nilai yang tipe dinamisnya bukan
`[]*schema.Message` — termasuk `nil` tanpa tipe.**

Untuk menyisipkan riwayat percakapan, template memakai
`schema.MessagesPlaceholder("history", true)`. Flag `true` (optional) hanya
menolong kalau **kuncinya tidak ada** di map input. Kalau kuncinya ada tapi
nilainya bertipe lain, node prompt langsung gagal:

```
only messages can be used to format message placeholder, key: history, actual type: <nil>
```

Jadi `map[string]any{"history": nil}` **tidak aman**. Yang aman ada dua:
hilangkan kuncinya sama sekali, atau isi dengan slice bertipe benar — nil slice
`([]*schema.Message)(nil)` sendiri aman, karena tipe dinamisnya tetap
`[]*schema.Message`. Di proyek ini kuncinya hanya dipasang kalau riwayatnya
memang berisi (`internal/rag/chain.go` → `prepareContext`).

**12. Untuk dokumen ini, xberg→Markdown tetap yang terbaik — tapi wajib
`--pdf-extract-tables false`.**

Empat cara ekstraksi diuji pada PDF yang sama (134 halaman, hasil Word 2016,
punya text layer). Ukurannya: berapa Pasal yang dikenali pemotong sadar-struktur
(harusnya 155), dan berapa baris yang isinya cuma satu kata (tanda layout
rusak).

| Cara | Pasal dikenali | Blok tabel palsu | Baris satu kata |
| --- | --- | --- | --- |
| xberg, tanpa flag | 153/155 | 9 dari 17 | 0% |
| **xberg + `--pdf-extract-tables false`** | **155/155** | **0** | **0%** |
| xberg + `--layout --use-layout-for-markdown` | 155/155 | 17 | 0% |
| `pdftotext -layout` | 155/155 | – | 0% |
| `pdftotext` biasa | 141/155 | – | 37% |
| `pdfparser` Eino (DOC_PATH diarahkan ke PDF) | – | – | pecah di tengah kata |

Yang perlu dicatat:

- `pdftotext` biasa **merusak** dokumen ini: 1.528 dari 4.114 baris berisi satu
  kata, karena baris justify dipecah per kata.
- `pdfparser` Eino paling buruk untuk dokumen berstruktur: ia memotong di tengah
  kata dan angka, mis. `Pasal 8` lalu `7` di baris berikutnya. Itu asal penanda
  `Pasal 8` palsu.
- `--layout` memakan 32 detik (vs 0,8 detik) dan **tidak** memperbaiki apa pun:
  jumlah blok tabelnya tetap 17.
- `pdftotext -layout` sebenarnya juga benar urutannya, tapi prosanya terpotong
  hard-wrap dan diberi padding spasi justifikasi. Bagus sebagai **rujukan
  tambalan** (lihat bagian "Mengganti dokumen"), bukan sebagai sumber utama.

Kesimpulannya: struktur dokumen ini lebih terjaga lewat Markdown, jadi
membangun ulang tanpa konversi akan memperburuk hasil.

---

## Struktur proyek

```
rag-eino/
├── cmd/rag/
│   ├── main.go                  entrypoint CLI + lima subcommand
│   └── markdown.go              penataan Markdown → ANSI untuk terminal
├── internal/
│   ├── config/config.go         baca .env dan environment variable
│   ├── gemini/
│   │   ├── gemini.go            chat model (Gemini / OpenAI-compatible) + dua embedder
│   │   └── retry.go             pengulangan otomatis saat kuota tercapai
│   ├── ingest/
│   │   ├── ingest.go            jalur tulis: siapkan → baca → potong → embed → simpan
│   │   └── pasal.go             splitter sadar-struktur (batas Pasal/BAB/Lampiran)
│   ├── rag/
│   │   ├── chain.go             jalur cepat: condense + retriever + prompt + graph
│   │   ├── penjawab.go          kontrak jalur baca + pilih jalur + cadangan
│   │   ├── agent.go             jalur agen: ReAct terbatas dengan tool
│   │   ├── tools.go             tool agen: cari_vektor, baca_pasal, ikuti_rujukan
│   │   ├── retriever.go         tambahan: ambil langsung pasal yang disebut,
│   │   │                        lalu ikuti rujukan silangnya satu lompatan
│   │   ├── cari.go              retrieval saja, untuk memeriksa hasil pencarian
│   │   ├── saran.go             saran pertanyaan lanjutan (web saja)
│   │   ├── sumber.go            label rujukan potongan ("Pasal 7", "Lampiran")
│   │   └── *_test.go            verifikasi wiring, streaming, memori, dan agen
│   └── web/
│       ├── server.go            handler HTTP + SSE + sesi percakapan
│       ├── templates/           halaman, fragmen, dan pesan singkat HTML
│       └── static/              Tailwind (sumber + hasil) dan htmx
├── package.json                 skrip build:css / vendor:js
└── README.md
```

## Konfigurasi

Semua pengaturan dibaca dari `.env`. Daftar lengkapnya ada di `.env.example`.

| Variabel | Default | Keterangan |
|---|---|---|
| `GEMINI_API_KEY` | — | **wajib**; dipakai chat (kalau `CHAT_PROVIDER=gemini`) dan selalu dipakai embedding |
| `GEMINI_CHAT_MODEL` | `gemini-3.5-flash-lite` | model penyusun jawaban; ketika `CHAT_PROVIDER=openai`, isi nama model yang dikenali proxy |
| `GEMINI_EMBED_MODEL` | `gemini-embedding-2` | model embedding |
| `GEMINI_EMBED_DIM` | `768` | dimensi vector |
| `CHAT_PROVIDER` | `gemini` | `gemini` = API resmi Google; `openai` = endpoint OpenAI-compatible. Lihat [Mode proxy](#mode-proxy-chat-model-opsional) |
| `CHAT_BASE_URL` | `http://localhost:8081/v1` | alamat endpoint OpenAI-compatible; hanya dipakai `CHAT_PROVIDER=openai` |
| `CHAT_API_KEY` | `sk-gemini` | kunci endpoint OpenAI-compatible; hanya dipakai `CHAT_PROVIDER=openai` |
| `QDRANT_HOST` / `QDRANT_PORT` | `localhost` / `6334` | port gRPC |
| `QDRANT_COLLECTION` | `pearson_ai` | nama collection |
| `DOC_PATH` | `data/ai_pv.pdf` | berkas dokumen sumber; `.pdf` di-parse sebagai PDF, selebihnya dibaca sebagai teks |
| `DOC_URL` | — (kosong) | opsional; diunduh hanya kalau `DOC_PATH` belum ada |
| `APP_TITLE` | `Tanya Dokumen` | judul halaman web |
| `APP_SUBTITLE` | `RAG dengan Eino, Gemini, dan Qdrant` | keterangan di bawah judul |
| `CHUNK_STRATEGY` | `pasal` | `pasal` = potong pada batas Pasal; `recursive` = potong berdasarkan ukuran saja |
| `CHUNK_SIZE` | `500` | ambang pemecahan; pada `pasal`: pasal yang muat dibiarkan utuh |
| `CHUNK_OVERLAP` | `100` | hanya dipakai `CHUNK_STRATEGY=recursive` |
| `TOP_K` | `10` | jumlah chunk yang diambil |
| `HISTORY_MESSAGES` | `6` | jumlah pesan riwayat percakapan yang disertakan; `0` mematikan memori |
| `SARAN_COUNT` | `3` | jumlah tombol saran pertanyaan lanjutan di client web; `0` mematikan fitur. Tidak dipakai `rag ask` |
| `AGENT_MODE` | `false` | `true` mengganti jalur baca menjadi agen ReAct; `false` memakai chain linear. Lihat [Mode agen](#mode-agen-eksperimental) |
| `AGENT_MAX_STEP` | `8` | batas langkah agen (satu panggilan model atau satu tool = satu langkah) |
| `AGENT_TIMEOUT_SECONDS` | `90` | jaring pengaman waktu per permintaan di jalur agen |

### Mengganti dokumen

Arahkan `DOC_PATH` ke dokumen lain. Cara membacanya ditentukan ekstensi berkas:

- `.pdf` — di-parse sebagai PDF, satu Document per halaman.
- lainnya (`.md`, `.txt`) — dibaca sebagai teks apa adanya.

Untuk dokumen yang berisi judul, tabel, atau penomoran seperti peraturan dan
naskah dinas, hasilnya jauh lebih rapi kalau PDF-nya dikonversi dulu ke
Markdown dengan **xberg**, lalu Markdown-nya yang di-ingest. xberg menjaga
struktur judul dan mengubah tabel menjadi tabel Markdown, sehingga potongan
teks yang masuk ke Qdrant lebih utuh daripada hasil ekstraksi PDF mentah.

```bash
# Sekali saja per dokumen. --disable-ocr true karena PDF-nya berbasis teks
# (hasil Word); untuk PDF hasil scan, biarkan OCR menyala.
xberg extract data/perbup_18_2025.pdf --content-format markdown \
  --disable-ocr true --pdf-extract-tables false > data/perbup_18_2025.md
```

Lalu set `DOC_PATH=data/perbup_18_2025.md`.

#### Kenapa `--pdf-extract-tables false`

Tanpa flag itu, pengenalan tabel xberg memberi **positif palsu** pada dokumen
ini: paragraf biasa dikira tabel dua kolom. Contohnya Pasal 25 yang utuh di PDF
keluar sebagai potongan sel:

```markdown
| Pasal 25 |  |
| --- | --- |
| (1) Naskah Dinas | korespondensi eksternal disusun dalam |
| bentuk surat dinas. |  |
```

Akibatnya berantai: penandanya berubah jadi `| Pasal 25 |` sehingga tidak lagi
di awal baris dan **Pasal 25 serta 131 hilang** dari hasil pemotongan, sementara
prosanya tercabik jadi sel. Dari 17 blok tabel di Markdown lama, 9 di antaranya
palsu (Pasal 25, 28, 78, 103, 106, 131, 145, dan sekitarnya).

Dengan flag itu, seluruh 155 Pasal dikenali dan tidak ada satu pun blok tabel
palsu. Tabel **asli** di Lampiran — mis. daftar "JABATAN → jenis naskah yang
dapat ditandatangani" — tetap ikut terbaca sebagai teks biasa, malah lebih
menguntungkan pemotongan karena setiap butir berdiri di barisnya sendiri.

Perbandingan lengkap cara ekstraksi yang pernah diuji ada di
[Catatan penting](#catatan-penting) nomor 12.

#### Satu tambalan manual: Pasal 89-90

Ada satu region yang tidak selesai oleh flag apa pun. Bagian amplop di halaman
34-36 ditulis rata kiri-kanan (justify), dan xberg membaca kata-katanya
berselang-seling antarbaris:

```markdown
- a. putih untuk sebagaimana dimaksud
- b. cokelat untuk sebagaimana dimaksud naskah dinas sebagaimana dimaksud
  amplop dalam amplop dalam dan dan lebar 14 cm kebutuhan kertas naskah

Pasal 8 naskah
```

`--pdf-extract-tables false` tidak menolong di sini (keluarannya sama persis),
dan `--layout` juga tidak. Yang benar adalah `pdftotext -layout` halaman 34-36 —
di sana urutan bacanya utuh.

Jadi Markdown hasil konversi **ditambal manual** untuk Pasal 89 dan 90. Kalau
Markdown dikonversi ulang dengan xberg, tambalan itu **hilang** dan harus
dikerjakan lagi:

```bash
# Ambil teks yang urutannya benar sebagai rujukan tambalan.
pdftotext -layout -f 34 -l 36 data/perbup_18_2025.pdf /tmp/hal34-36.txt
```

Perhatikan juga: region teracak itu memunculkan penanda `Pasal 8` palsu di
tengah Pasal 89. Dulu potongan berlabel `pasal=8` sempat berisi kata-kata tak
berhubungan; penjagaan nomor monoton di `bagiSegmen` menolaknya, tapi tambalan
ini yang menghapus sumbernya.

Tambalan dijaga oleh `TestPasal89Dan90TidakTeracakDiDokumenSungguhan` di
`internal/ingest/pasal_test.go`. Test itu **sengaja gagal** kalau Markdown
dikonversi ulang tanpa ditambal.

`DOC_URL` hanya perlu diisi kalau dokumennya mau diunduh otomatis dan belum ada
di disk. Dikosongkan berarti dokumen dianggap sudah disiapkan lokal.

### Mengulang dari awal

`ingest` bersifat idempotent dan inkremental — menjalankan ulang dokumen yang
sama akan mengenali chunk yang sudah tersimpan dan melewatinya, jadi data tidak
menumpuk dan kuota tidak terbuang.

Konsekuensinya: kalau **dokumennya diganti**, chunk dari dokumen lama tidak
otomatis terhapus — ID-nya berbeda, jadi ia tetap tertinggal di collection dan
bisa ikut terjawab saat `ask`. Hal yang sama berlaku kalau **parameter
pemotongan diubah** (`CHUNK_STRATEGY`, `CHUNK_SIZE`), karena ID chunk
diturunkan dari isinya.

Untuk mengganti dokumen atau mengubah pemotongan, hapus collection-nya dulu
supaya benar-benar dibangun ulang dari nol:

```bash
curl -X DELETE localhost:6333/collections/perbup_18_2025
go run ./cmd/rag ingest
```

Kalau lupa menghapusnya, tidak ada yang rusak — hanya saja hasil pencarian
masih bercampur dengan potongan lama. Metadata `sidik_jari` pada setiap
potongan membuat keadaan ini bisa dikenali.

Cara yang lebih aman untuk mengganti topik: biarkan collection lama utuh dan
arahkan `QDRANT_COLLECTION` ke nama baru. Collection lama tetap bisa dipakai
lagi dengan mengembalikan nilainya, tanpa perlu embed ulang.

Kalau `GEMINI_EMBED_DIM` diubah sementara collection lama masih ada, `ingest`
akan menolak dengan pesan yang menjelaskan dimensi yang tidak cocok — vector
dengan dimensi berbeda tidak bisa dibandingkan.

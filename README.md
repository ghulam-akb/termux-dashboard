# Termux System Dashboard & Remote Console (Go SSL Engine)

Aplikasi web dashboard sistem untuk Termux Android yang dikembangkan menggunakan **Go (Golang)** berkinerja tinggi. Dashboard ini menampilkan statistik hardware secara real-time, mengizinkan penjelajahan berkas, pemantauan sistem, pemicuan API perangkat Android (kamera, lokasi, SMS), serta memiliki **Interactive WebTTY (Terminal)** terintegrasi yang aman untuk mengontrol Termux dari jarak jauh menggunakan enkripsi SSL/TLS.

---

## ✨ Fitur Utama

1. **Glassmorphism Web Dashboard**: Antarmuka modern bertema gelap dengan animasi dinamis (Battery Percent, RAM usage, storage space, uptime, dll).
2. **Single-Binary Execution**: Seluruh file HTML/CSS/JS dibundel ke dalam file biner `dashboard` (~9.8MB) menggunakan fitur Go `embed`. Anda dapat menjalankan binary ini di mana saja tanpa folder `public` eksternal.
3. **HTTPS / SSL Encrypted**: Komunikasi data dienkripsi menggunakan protokol HTTPS (TLS) secara bawaan. Sertifikat SSL *self-signed* (`cert.pem` & `key.pem`) akan dibuat otomatis saat server pertama kali dijalankan.
4. **HTTP Basic Authentication**: Melindungi dashboard dari akses luar menggunakan login Username & Password.
5. **Interactive WebTTY**: Konsol terminal interaktif berbasis WebSockets dan `xterm.js` yang terhubung langsung ke shell PTY (`/data/data/com.termux/files/usr/bin/bash` atau `sh`). Mendukung penyesuaian ukuran layout dinamis (seperti menjalankan `nano`, `htop`, dll).
6. **File Explorer (Penjelajah Berkas)**: Menjelajahi file sistem Termux langsung dari web, mendukung navigasi folder, unduh berkas, hapus berkas, dan unggah berkas (hingga ukuran 50MB via Multipart Upload).
7. **Android Hardware Integration (`termux-api`)**:
   - **Kamera HP**: Mengambil foto secara langsung menggunakan kamera utama/belakang perangkat Android (`termux-camera-photo`) dan menampilkan hasilnya langsung di dashboard.
   - **GPS Tracker**: Mendapatkan informasi koordinat lokasi (Latitude, Longitude, Altitude, Akurasi) menggunakan `termux-location` dan menampilkannya secara interaktif pada peta Leaflet.js.
   - **SMS Reader**: Membaca dan menampilkan daftar 5 SMS masuk terakhir dari perangkat menggunakan `termux-sms-list`.
8. **Logger Riwayat Koneksi**: Setiap koneksi dari IP luar secara otomatis dicatat ke `connections.log` untuk monitoring keamanan.
9. **Sistem Keamanan IP (Whitelist & Blacklist)**: Membatasi akses dashboard hanya untuk IP tertentu menggunakan file konfigurasi eksternal (`whitelist.txt` dan `blacklist.txt`).
10. **Otorisasi Interaktif (Notifikasi & Konfirmasi)**: Menampilkan notifikasi Android dan dialog konfirmasi (Yes/No) di layar ponsel ketika perangkat baru mencoba terhubung (hanya setelah lolos login Basic Auth).

---

## 📂 Struktur Berkas

- `main.go` — Kode backend server HTTP Go, handler API, WebTTY, dan integrasi Android API.
- `public/index.html` — Berkas frontend statis (HTML/CSS/JS).
- `dashboard` — Executable biner hasil kompilasi.
- `cert.pem` & `key.pem` — Sertifikat SSL untuk enkripsi HTTPS (dibuat otomatis).
- `connections.log` — File log pencatatan riwayat IP yang mengakses dashboard (dibuat otomatis).
- `whitelist.txt` — Daftar IP/Subnet yang diizinkan masuk.
- `blacklist.txt` — Daftar IP/Subnet yang diblokir masuk.

---

## 🔐 Konfigurasi Keamanan

### 1. Kredensial Login (Basic Auth)
Secara bawaan, kredensial masuk adalah:
* **Username**: `admin`
* **Password**: `termux`

Anda dapat mengubah username dan password ini dengan menetapkan *environment variables* saat menjalankan aplikasi:
```bash
DASHBOARD_USER=nama_anda DASHBOARD_PASSWORD=sandi_rahasia ./dashboard
```

### 2. HTTPS / SSL Certificate
Aplikasi akan secara otomatis membuat `cert.pem` dan `key.pem` yang berlaku selama 1 tahun. 
* Saat membuka halaman web untuk pertama kali (misal: `https://localhost:8443`), peramban (browser) akan menampilkan peringatan keamanan *"Your connection is not private"* (karena sertifikat dibuat sendiri/self-signed).
* **Solusi**: Klik tombol **Advanced** / **Lanjutan** lalu pilih **Proceed to ... (unsafe)** / **Lanjutkan ke ... (tidak aman)**. Semua komunikasi Anda kini terenkripsi secara aman.

### 3. Otorisasi Alur Koneksi
Sistem memproses keamanan dalam 3 tahap:
1. **IP Filter**: IP dicocokkan dengan `blacklist.txt` dan `whitelist.txt`. Jika diblokir, akses ditolak langsung.
2. **Basic Auth**: Pengguna diminta memasukkan username dan password.
3. **Android Dialog Prompt**: Setelah password benar, layar HP Android pemilik Termux akan memunculkan dialog popup konfirmasi untuk mengizinkan/menolak perangkat tersebut.

---

## 🔌 API Endpoints Reference

### File Manager APIs
* **`GET /api/files/list?path=<dir>`**: Mengambil daftar file dan folder pada path yang ditentukan (default `.`).
* **`GET /api/files/download?path=<file>`**: Mengunduh berkas terpilih dari Termux ke client.
* **`POST /api/files/upload?path=<dir>`**: Mengunggah file ke direktori tujuan (Multipart Form, batas maks 50MB).
* **`POST /api/files/delete`**: Menghapus file/folder (Body: JSON `{"path": "<target_path>"}`).

### Android Hardware APIs (Memerlukan `termux-api`)
* **`POST /api/android/photo`**: Menjepret foto menggunakan kamera utama perangkat (kamera belakang ID 0).
* **`GET /api/android/photo/view`**: Menampilkan atau mengunduh hasil foto terakhir (`captured_temp.jpg`).
* **`GET /api/android/location`**: Meminta koordinat GPS terkini dari satelit/jaringan ponsel.
* **`GET /api/android/sms`**: Membaca daftar pesan teks masuk terkini.

---

## 🚀 Cara Menjalankan & Menghentikan Dashboard

### Prasyarat
Untuk menggunakan fitur Android Hardware Integration (Kamera, Lokasi, SMS, Vibrate, TTS, dll), pastikan aplikasi **Termux:API** terinstal dari F-Droid dan paket API terpasang di dalam terminal:
```bash
pkg install termux-api
```

### 1. Menjalankan di Depan Layar (Foreground)
```bash
cd ~/termux-dashboard
./dashboard
```
*Gunakan pintasan **`Ctrl + C`** untuk menghentikan.*

### 2. Menjalankan di Latar Belakang (Background) — *Direkomendasikan*
```bash
cd ~/termux-dashboard
nohup ./dashboard > /dev/null 2>&1 &
```

### 3. Menghentikan Layanan di Latar Belakang
```bash
pkill dashboard
```

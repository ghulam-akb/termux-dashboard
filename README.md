# Termux System Dashboard & Remote Console (Go SSL Engine)

Aplikasi web dashboard sistem untuk Termux Android yang dikembangkan menggunakan **Go (Golang)** berkinerja tinggi. Dashboard ini menampilkan statistik hardware secara real-time, mengizinkan pemicuan fitur diagnostik ponsel, serta memiliki **Remote Terminal** terintegrasi yang aman untuk mengontrol Termux dari jarak jauh menggunakan enkripsi SSL/TLS.

---

## ✨ Fitur Utama

1. **Glassmorphism Web Dashboard**: Antarmuka modern bertema gelap dengan animasi dinamis (Battery Percent, RAM usage, storage space, uptime, dll).
2. **Single-Binary Execution**: Seluruh file HTML/CSS/JS dibundel ke dalam file biner `dashboard` (~9.8MB) menggunakan fitur Go `embed`. Anda dapat menjalankan binary ini di mana saja tanpa folder `public` eksternal.
3. **HTTPS / SSL Encrypted**: Komunikasi data dienkripsi menggunakan protokol HTTPS (TLS) secara bawaan. Sertifikat SSL *self-signed* (`cert.pem` & `key.pem`) akan dibuat otomatis saat server pertama kali dijalankan.
4. **HTTP Basic Authentication**: Melindungi dashboard dari akses luar menggunakan login Username & Password.
5. **Remote Terminal Console**: Antarmuka CLI monospaced retro untuk mengirim dan menjalankan command shell Termux langsung dari browser.
6. **Quick Shell Actions**: Kartu kendali cepat di dashboard untuk menjalankan aksi penting seperti menyalakan/mematikan server SSH (`sshd`), melihat proses yang berjalan (`top`), dan menampilkan berkas penyimpanan (`ls`).
7. **Logger Riwayat Koneksi**: Setiap koneksi dari IP luar secara otomatis dicatat ke `connections.log` untuk monitoring keamanan.
8. **Sistem Keamanan IP (Whitelist & Blacklist)**: Membatasi akses dashboard hanya untuk IP tertentu menggunakan file konfigurasi eksternal.
9. **Otorisasi Interaktif (Notifikasi & Konfirmasi)**: Menampilkan notifikasi Android dan dialog konfirmasi (Yes/No) di layar ponsel ketika perangkat baru mencoba terhubung (hanya setelah lolos login Basic Auth).

---

## 📂 Struktur Berkas

- `main.go` — Kode backend server HTTP Go, handler API, dan logger.
- `public/index.html` — Berkas frontend statis (HTML/CSS/JS).
- `dashboard` — Executable biner hasil kompilasi.
- `cert.pem` & `key.pem` — Sertifikat SSL untuk enkripsi HTTPS (dibuat otomatis).
- `connections.log` — File log pencatatan riwayat IP yang mengakses dashboard.
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

## 🚀 Cara Menjalankan & Menghentikan Dashboard

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

---

## 🎛️ Fitur Quick Shell Actions

Pada halaman depan dashboard, terdapat kartu **Quick Shell Actions** yang dapat Anda ketuk untuk mengeksekusi perintah penting secara cepat:
* **Start SSH Server (sshd)**: Menyalakan server SSH di Termux agar Anda bisa login dari PC menggunakan SSH client.
* **Stop SSH Server**: Mematikan semua proses server SSH (`sshd`).
* **Monitor Active Processes**: Menampilkan daftar 15 proses teratas yang memakan memori/CPU di Termux Anda.
* **List Storage Files**: Menampilkan isi direktori penyimpanan Anda saat ini (`ls -lh`).

*Setiap kali tombol diketuk, aplikasi akan otomatis memindahkan Anda ke tab **Terminal** dan mengalirkan keluaran perintah secara real-time.*

# Termux System Dashboard & Remote Console

Aplikasi web dashboard untuk Termux Android menggunakan Go. Aplikasi ini berfungsi menampilkan metrik perangkat, mengelola file, mengeksekusi perintah diagnostik cepat, dan menyediakan akses terminal remote terenkripsi SSL/TLS.

---

## Fitur Utama

1. **Dashboard Metrik**: Menampilkan status baterai, penggunaan RAM, sisa penyimpanan, dan uptime perangkat.
2. **Biner Tunggal (Single-Binary)**: Semua file statis frontend (HTML, CSS, JS) di-embed ke dalam biner Go menggunakan `go:embed`. Aplikasi dapat dijalankan langsung sebagai satu file executable.
3. **Koneksi HTTPS**: Menggunakan SSL/TLS secara default. Sertifikat `cert.pem` dan `key.pem` dibuat otomatis saat server pertama kali dijalankan.
4. **Autentikasi Basic**: Akses dashboard dilindungi oleh HTTP Basic Authentication.
5. **Terminal WebTTY**: Terminal remote interaktif menggunakan WebSockets dan `xterm.js` yang terhubung langsung ke shell PTY (`bash` atau `sh`). Mendukung penyesuaian ukuran terminal.
6. **Manajemen File (File Explorer)**: Navigasi direktori, unduh, hapus, dan unggah file (ukuran maksimal 50MB).
7. **Keamanan IP**: Filter akses menggunakan berkas konfigurasi `whitelist.txt` dan `blacklist.txt`.
8. **Konfirmasi Koneksi Android**: Menampilkan notifikasi dan dialog konfirmasi persetujuan di layar HP ketika ada percobaan koneksi baru dari IP eksternal.
9. **Log Koneksi**: Mencatat setiap aktivitas koneksi masuk ke dalam file `connections.log`.
10. **Android Wake Lock**: Otomatis mengaktifkan wake lock (`termux-wake-lock`) untuk mencegah CPU Android tidur saat server berjalan, sehingga koneksi background tetap stabil.

---

## Struktur Direktori

- `main.go` — Kode backend server HTTP Go, middleware keamanan, dan WebTTY.
- `public/index.html` — Frontend statis (HTML/CSS/JS).
- `dashboard` — Executable biner hasil kompilasi.
- `LICENSE` — Berkas lisensi MIT untuk proyek.
- `cert.pem` & `key.pem` — Sertifikat SSL untuk enkripsi HTTPS (dibuat otomatis).
- `connections.log` — Log riwayat IP yang mengakses dashboard (dibuat otomatis).
- `whitelist.txt` — Daftar IP/Subnet yang diizinkan mengakses dashboard.
- `blacklist.txt` — Daftar IP/Subnet yang diblokir dari dashboard.

---

## Konfigurasi Keamanan

### 1. Kredensial Login
* **Username**: `admin` (Dapat diubah dengan mendefinisikan `DASHBOARD_USER`).
* **Password**: Secara default, jika *environment variable* `DASHBOARD_PASSWORD` tidak ditentukan, aplikasi akan **membuat password acak sekali pakai secara otomatis** pada saat startup dan mencetaknya ke konsol terminal Termux.

Untuk menetapkan kredensial secara manual, jalankan aplikasi dengan *environment variables* berikut:
```bash
DASHBOARD_USER=username_baru DASHBOARD_PASSWORD=password_baru ./dashboard
```

### 2. Sertifikat SSL
Biner akan membuat berkas `cert.pem` dan `key.pem` secara otomatis.
* Saat mengakses pertama kali via peramban (misalnya `https://localhost:8443`), peramban akan memunculkan peringatan sertifikat tidak dikenal (*self-signed*).
* Anda dapat melanjutkan akses secara aman (*proceed unsafe*) karena koneksi tetap terenkripsi.

### 3. Alur Verifikasi Koneksi
1. Verifikasi IP pada `blacklist.txt` dan `whitelist.txt`.
2. Verifikasi HTTP Basic Authentication.
3. Dialog konfirmasi Android (khusus koneksi dari luar localhost).

---

## API Reference

### File Manager
* **`GET /api/files/list?path=<direktori>`**: Menampilkan daftar file dan folder (default ke direktori aktif `.`).
* **`GET /api/files/download?path=<file>`**: Mengunduh berkas.
* **`POST /api/files/upload?path=<direktori>`**: Mengunggah berkas ke direktori tujuan (batas ukuran 50MB).
* **`POST /api/files/delete`**: Menghapus berkas atau folder (Menerima JSON: `{"path": "<path_berkas>"}`).

### Perintah Cepat (Device Diagnostics)
* **`POST /api/vibrate`**: Mengaktifkan getar ponsel (memerlukan `termux-api`).
* **`POST /api/tts`**: Mengucapkan teks lewat speaker (memerlukan `termux-api`, parameter JSON: `{"text": "<teks>"}`).
* **`POST /api/toast`**: Menampilkan notifikasi toast di layar HP (memerlukan `termux-api`, parameter JSON: `{"text": "<teks>"}`).

---

## Panduan Penggunaan

### Menjalankan Server (Foreground)
```bash
cd ~/termux-dashboard
./dashboard
```
Tekan `Ctrl + C` untuk menghentikan.

### Menjalankan di Latar Belakang (Background)
```bash
cd ~/termux-dashboard
nohup ./dashboard > /dev/null 2>&1 &
```

### Menghentikan Proses Server

```bash
pkill dashboard
```

---

## Lisensi

Proyek ini dilisensikan di bawah [Lisensi MIT](LICENSE).

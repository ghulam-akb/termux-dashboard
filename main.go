package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

//go:embed public/*
var embedFS embed.FS

const defaultPort = "8443"

// Request/Response structs
type BatteryInfo struct {
	Percentage  int     `json:"percentage"`
	Temperature float64 `json:"temperature"`
	Health      string  `json:"health"`
	Plugged     string  `json:"plugged"`
	Status      string  `json:"status"`
}

type RAMInfo struct {
	Total     int `json:"total"`
	Used      int `json:"used"`
	Free      int `json:"free"`
	Available int `json:"available"`
}

type StorageInfo struct {
	Size    string `json:"size"`
	Used    string `json:"used"`
	Avail   string `json:"avail"`
	Percent string `json:"percent"`
}

type StatsResponse struct {
	Battery BatteryInfo `json:"battery"`
	RAM     RAMInfo     `json:"ram"`
	Storage StorageInfo `json:"storage"`
	Uptime  string      `json:"uptime"`
}

type TextPayload struct {
	Text string `json:"text"`
}

type CommandPayload struct {
	Command string `json:"command"`
}

type CommandResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
	Error    string `json:"error,omitempty"`
}

// File Manager Structs
type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mtime"`
}

type FileDeletePayload struct {
	Path string `json:"path"`
}

// IP Access Confirmation Cache Types
type IPApprovalState struct {
	Waiters []chan bool
}

// Global state for IP confirmation, Auth, and WS Tokens
var (
	approvedIPs      = make(map[string]time.Time)
	pendingApprovals = make(map[string]*IPApprovalState)
	approvalMutex    sync.Mutex

	basicUser = "admin"
	basicPass = "termux"

	wsTokens      = make(map[string]time.Time)
	wsTokensMutex sync.Mutex
)

// WebSocket Upgrader
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func initAuth() {
	if u := os.Getenv("DASHBOARD_USER"); u != "" {
		basicUser = u
	}
	if p := os.Getenv("DASHBOARD_PASSWORD"); p != "" {
		basicPass = p
	}
}

func main() {
	initAuth()

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	// Generate SSL certificates if missing
	if err := checkOrGenerateCert(); err != nil {
		fmt.Printf("Error checking/generating SSL certificate: %v\n", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()

	// API Endpoints
	mux.HandleFunc("GET /api/stats", handleStats)
	mux.HandleFunc("POST /api/vibrate", handleVibrate)
	mux.HandleFunc("POST /api/tts", handleTTS)
	mux.HandleFunc("POST /api/toast", handleToast)
	mux.HandleFunc("POST /api/execute", handleExecute)

	// File Manager Endpoints
	mux.HandleFunc("GET /api/files/list", handleFileList)
	mux.HandleFunc("GET /api/files/download", handleFileDownload)
	mux.HandleFunc("POST /api/files/upload", handleFileUpload)
	mux.HandleFunc("POST /api/files/delete", handleFileDelete)

	// WebSocket / Token Endpoints
	mux.HandleFunc("POST /api/token", handleGetToken)
	mux.HandleFunc("GET /ws/terminal", handleTerminalWS)

	// Static file handler (with local folder fallback)
	mux.Handle("/", getStaticFileHandler())

	// Print startup information
	printStartupInfo(port)

	// Start HTTPS server wrapped with connection logger, IP access, and Basic Auth middleware
	serverAddr := ":" + port
	if err := http.ListenAndServeTLS(serverAddr, "cert.pem", "key.pem", loggingMiddleware(mux)); err != nil {
		fmt.Printf("Error starting secure HTTPS server: %v\n", err)
		os.Exit(1)
	}
}

// Logging and Security Middleware
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract Client IP
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

		// Check if proxied
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ip = strings.Split(xff, ",")[0]
		}

		// Step A: Check Whitelist & Blacklist Access
		allowed, reason := checkIPAccess(ip)
		if !allowed {
			logBlockedAttempt(ip, r, reason)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "Access Denied: Your IP (%s) is not authorized. Reason: %s\n", ip, reason)
			return
		}

		// Step B: Check HTTP Basic Authentication (skip for WebSocket endpoint)
		if r.URL.Path != "/ws/terminal" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != basicUser || pass != basicPass {
				w.Header().Set("WWW-Authenticate", `Basic realm="Secure Termux Dashboard"`)
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte("401 Unauthorized - Access Denied\n"))
				return
			}
		}

		// Step C: Check Interactive Android Confirmation Dialog (Only for authenticated requests)
		allowedInt, reasonInt := checkIPInteractive(ip)
		if !allowedInt {
			logBlockedAttempt(ip, r, reasonInt)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "Access Denied: Connection rejected by the device owner.\n")
			return
		}

		logConnection(ip, r)
		next.ServeHTTP(w, r)
	})
}

// Generate secure one-time WS token
func generateWSToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	token := fmt.Sprintf("%x", b)

	wsTokensMutex.Lock()
	wsTokens[token] = time.Now().Add(15 * time.Second) // Valid for 15s
	wsTokensMutex.Unlock()

	return token
}

// Validate and consume WS token
func validateWSToken(token string) bool {
	wsTokensMutex.Lock()
	defer wsTokensMutex.Unlock()

	expiry, exists := wsTokens[token]
	if !exists {
		return false
	}
	delete(wsTokens, token) // One-time use

	return time.Now().Before(expiry)
}

// Handlers for WebTTY WebSocket Shell
func handleGetToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	token := generateWSToken()
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

func handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !validateWSToken(token) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized - Invalid Token"))
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("WS Upgrade error: %v\n", err)
		return
	}
	defer conn.Close()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/data/data/com.termux/files/usr/bin/bash"
		if _, err := os.Stat(shell); os.IsNotExist(err) {
			shell = "sh"
		}
	}

	c := exec.Command(shell)
	c.Env = append(os.Environ(), "TERM=xterm-256color")

	f, err := pty.Start(c)
	if err != nil {
		fmt.Printf("PTY Start error: %v\n", err)
		return
	}
	defer f.Close()
	defer c.Process.Kill()

	// Pipe PTY stdout/stderr -> WebSocket
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := f.Read(buf)
			if err != nil {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				return
			}
		}
	}()

	// Pipe WebSocket -> PTY stdin
	for {
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data,omitempty"`
			Rows uint16 `json:"rows,omitempty"`
			Cols uint16 `json:"cols,omitempty"`
		}
		err := conn.ReadJSON(&msg)
		if err != nil {
			break
		}

		switch msg.Type {
		case "input":
			f.Write([]byte(msg.Data))
		case "resize":
			pty.Setsize(f, &pty.Winsize{
				Rows: msg.Rows,
				Cols: msg.Cols,
			})
		}
	}
}

// ----------------------------------------------------
// File Manager Handlers
// ----------------------------------------------------

func handleFileList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/data/data/com.termux/files/home"
	}
	path = filepath.Clean(path)

	entries, err := os.ReadDir(path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read directory: " + err.Error()})
		return
	}

	fileList := []FileEntry{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fileList = append(fileList, FileEntry{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	json.NewEncoder(w).Encode(fileList)
}

func handleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Path parameter missing"))
		return
	}
	path = filepath.Clean(path)

	// Send file download headers
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filepath.Base(path)))
	http.ServeFile(w, r, path)
}

func handleFileUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	r.Body = http.MaxBytesReader(w, r.Body, 50*1024*1024) // Limit upload to 50MB
	err := r.ParseMultipartForm(50 * 1024 * 1024)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "File size exceeds 50MB limit"})
		return
	}

	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		targetPath = "/data/data/com.termux/files/home"
	}
	targetPath = filepath.Clean(targetPath)

	file, handler, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read file from form: " + err.Error()})
		return
	}
	defer file.Close()

	destFile := filepath.Join(targetPath, handler.Filename)
	out, err := os.Create(destFile)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to create target file: " + err.Error()})
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to write file to disk: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Uploaded", "filename": handler.Filename})
}

func handleFileDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var payload FileDeletePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid payload"})
		return
	}

	path := filepath.Clean(payload.Path)
	if path == "/" || path == "/data" || path == "/data/data" || path == "/data/data/com.termux" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Deleting critical system directories is forbidden"})
		return
	}

	err := os.RemoveAll(path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to delete: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Deleted"})
}



// ----------------------------------------------------
// Security & IP Helpers
// ----------------------------------------------------

func checkIPAccess(clientIP string) (bool, string) {
	if blacklistBytes, err := os.ReadFile("blacklist.txt"); err == nil {
		lines := strings.Split(string(blacklistBytes), "\n")
		for _, line := range lines {
			if ipMatchesLine(clientIP, line) {
				return false, "Blacklisted"
			}
		}
	}

	if whitelistBytes, err := os.ReadFile("whitelist.txt"); err == nil {
		lines := strings.Split(string(whitelistBytes), "\n")
		hasRules := false
		inWhitelist := false
		
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			hasRules = true
			if ipMatchesLine(clientIP, line) {
				inWhitelist = true
				break
			}
		}
		
		if hasRules && !inWhitelist {
			return false, "Not in Whitelist"
		}
	}

	return true, ""
}

func checkIPInteractive(clientIP string) (bool, string) {
	if clientIP == "127.0.0.1" || clientIP == "::1" || clientIP == "localhost" {
		return true, ""
	}

	approvalMutex.Lock()
	if expiry, exists := approvedIPs[clientIP]; exists && time.Now().Before(expiry) {
		approvalMutex.Unlock()
		return true, ""
	}

	state, pending := pendingApprovals[clientIP]
	if pending {
		ch := make(chan bool, 1)
		state.Waiters = append(state.Waiters, ch)
		approvalMutex.Unlock()
		
		allowed := <-ch
		if allowed {
			return true, ""
		}
		return false, "Connection Rejected by User"
	}

	state = &IPApprovalState{}
	pendingApprovals[clientIP] = state
	approvalMutex.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exec.CommandContext(ctx, "termux-notification", "-t", "Dashboard Access Request", "-c", fmt.Sprintf("IP %s is trying to connect.", clientIP)).Run()
	}()

	allowed := askUserConfirmation(clientIP)

	approvalMutex.Lock()
	if allowed {
		approvedIPs[clientIP] = time.Now().Add(2 * time.Hour)
	}

	for _, ch := range state.Waiters {
		ch <- allowed
		close(ch)
	}

	delete(pendingApprovals, clientIP)
	approvalMutex.Unlock()

	if allowed {
		return true, ""
	}
	return false, "Connection Rejected by User"
}

func askUserConfirmation(ip string) bool {
	_, err := exec.LookPath("termux-dialog")
	if err != nil {
		fmt.Printf("[WARNING] termux-dialog tidak ditemukan di PATH. Mengizinkan koneksi dari IP %s secara default.\n", ip)
		return true
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "termux-dialog", "confirm", "-t", "Otorisasi Dashboard", "-i", fmt.Sprintf("Perangkat dengan IP %s mencoba terhubung ke dashboard Anda. Izinkan akses?", ip))
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			fmt.Printf("[SECURITY] Otorisasi IP %s otomatis ditolak karena tidak direspons selama 30 detik.\n", ip)
			return false
		}
		fmt.Printf("[WARNING] Gagal menjalankan termux-dialog: %v\n", err)
		return false
	}

	var result struct {
		Code int    `json:"code"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		outStr := strings.ToLower(string(output))
		return strings.Contains(outStr, "yes")
	}

	return result.Code == 0 || strings.ToLower(result.Text) == "yes"
}

func ipMatchesLine(clientIPStr, line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return false
	}

	if strings.Contains(line, "/") {
		_, ipNet, err := net.ParseCIDR(line)
		if err == nil {
			clientIP := net.ParseIP(clientIPStr)
			if clientIP != nil {
				return ipNet.Contains(clientIP)
			}
		}
	}

	return clientIPStr == line
}

func logConnection(ip string, r *http.Request) {
	if r.URL.Path == "/api/stats" {
		return
	}

	logLine := fmt.Sprintf("[%s] IP: %-15s - %-4s %s - UA: %s\n",
		time.Now().Format("2006-01-02 15:04:05"),
		ip,
		r.Method,
		r.URL.Path,
		r.Header.Get("User-Agent"),
	)

	fmt.Print(logLine)

	f, err := os.OpenFile("connections.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		f.WriteString(logLine)
	}
}

func logBlockedAttempt(ip string, r *http.Request, reason string) {
	logLine := fmt.Sprintf("[%s] BLOCKED: %-15s (%s) - %-4s %s - UA: %s\n",
		time.Now().Format("2006-01-02 15:04:05"),
		ip,
		reason,
		r.Method,
		r.URL.Path,
		r.Header.Get("User-Agent"),
	)

	fmt.Print(logLine)

	f, err := os.OpenFile("connections.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		defer f.Close()
		f.WriteString(logLine)
	}
}

func checkOrGenerateCert() error {
	certFile := "cert.pem"
	keyFile := "key.pem"

	_, err1 := os.Stat(certFile)
	_, err2 := os.Stat(keyFile)
	if err1 == nil && err2 == nil {
		return nil
	}

	fmt.Println("SSL certificate files missing. Generating self-signed TLS certificates...")

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Termux Go Dashboard"},
			CommonName:   "TermuxServer",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	template.IPAddresses = append(template.IPAddresses, net.ParseIP("127.0.0.1"), net.ParseIP("::1"))
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && !ip.IsLoopback() {
					template.IPAddresses = append(template.IPAddresses, ip)
				}
			}
		}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return err
	}
	defer certOut.Close()
	pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})

	fmt.Println("SSL self-signed certificate (cert.pem & key.pem) generated successfully.")
	return nil
}

// Handler functions
func handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	stats := StatsResponse{
		Battery: getBatteryInfo(),
		RAM:     getRAMInfo(),
		Storage: getStorageInfo(),
		Uptime:  getUptimeInfo(),
	}

	json.NewEncoder(w).Encode(stats)
}

func handleVibrate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "termux-vibrate", "-d", "500")
	if err := cmd.Run(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Vibrate failed: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Vibrated"})
}

func handleTTS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	r.Body = http.MaxBytesReader(w, r.Body, 1048576)

	var payload TextPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON payload"})
		return
	}

	text := payload.Text
	if text == "" {
		text = "Hello from Termux Dashboard"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "termux-tts-speak", text)
	if err := cmd.Run(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "TTS failed: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Spoken", "text": text})
}

func handleToast(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	r.Body = http.MaxBytesReader(w, r.Body, 1048576)

	var payload TextPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON payload"})
		return
	}

	text := payload.Text
	if text == "" {
		text = "Alert!"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "termux-toast", text)
	if err := cmd.Run(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Toast failed: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Toasted", "text": text})
}

func handleExecute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1048576)

	var payload CommandPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON payload"})
		return
	}

	command := strings.TrimSpace(payload.Command)
	if command == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Command is empty"})
		return
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/data/data/com.termux/files/usr/bin/bash"
		if _, err := os.Stat(shell); os.IsNotExist(err) {
			shell = "sh"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-c", command)
	
	outputBytes, err := cmd.CombinedOutput()
	outputStr := string(outputBytes)

	exitCode := 0
	var errMsg string
	if err != nil {
		errMsg = err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			errMsg = "Command execution timed out (30s limits)"
			exitCode = -1
		} else if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1
		}
	}

	response := CommandResponse{
		Output:   outputStr,
		ExitCode: exitCode,
		Error:    errMsg,
	}

	json.NewEncoder(w).Encode(response)
}

// System helpers
func getBatteryInfo() BatteryInfo {
	info := BatteryInfo{Status: "Unavailable", Health: "UNKNOWN", Plugged: "UNPLUGGED"}

	cmd := exec.Command("termux-battery-status")
	if output, err := cmd.Output(); err == nil {
		var result map[string]interface{}
		if err := json.Unmarshal(output, &result); err == nil {
			if pct, ok := result["percentage"].(float64); ok {
				info.Percentage = int(pct)
			}
			if temp, ok := result["temperature"].(float64); ok {
				info.Temperature = temp
			}
			if health, ok := result["health"].(string); ok {
				info.Health = health
			}
			if status, ok := result["status"].(string); ok {
				info.Status = status
			}
			if plug, ok := result["plugged"].(string); ok {
				info.Plugged = plug
			}
			return info
		}
	}

	if content, err := os.ReadFile("/sys/class/power_supply/battery/capacity"); err == nil {
		if val, err := strconv.Atoi(strings.TrimSpace(string(content))); err == nil {
			info.Percentage = val
		}
	}
	if content, err := os.ReadFile("/sys/class/power_supply/battery/temp"); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(content)), 64); err == nil {
			if val > 100 {
				info.Temperature = val / 10.0
			} else {
				info.Temperature = val
			}
		}
	} else if content, err := os.ReadFile("/sys/class/power_supply/battery/temperature"); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(content)), 64); err == nil {
			if val > 100 {
				info.Temperature = val / 10.0
			} else {
				info.Temperature = val
			}
		}
	}
	if content, err := os.ReadFile("/sys/class/power_supply/battery/status"); err == nil {
		info.Status = strings.TrimSpace(string(content))
		if strings.ToLower(info.Status) == "charging" {
			info.Plugged = "PLUGGED"
		}
	}
	if content, err := os.ReadFile("/sys/class/power_supply/battery/health"); err == nil {
		info.Health = strings.ToUpper(strings.TrimSpace(string(content)))
	}

	return info
}

func getRAMInfo() RAMInfo {
	info := RAMInfo{}

	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return info
	}

	lines := strings.Split(string(content), "\n")
	var memTotal, memFree, memAvailable int

	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := parts[0]
		val, _ := strconv.Atoi(parts[1])

		switch key {
		case "MemTotal:":
			memTotal = val / 1024
		case "MemFree:":
			memFree = val / 1024
		case "MemAvailable:":
			memAvailable = val / 1024
		}
	}

	info.Total = memTotal
	info.Free = memFree
	
	if memAvailable == 0 {
		info.Available = memFree
	} else {
		info.Available = memAvailable
	}
	
	info.Used = memTotal - info.Available
	return info
}

func getStorageInfo() StorageInfo {
	info := StorageInfo{Size: "0", Used: "0", Avail: "0", Percent: "0%"}

	var stat syscall.Statfs_t
	err := syscall.Statfs("/data", &stat)
	if err != nil {
		err = syscall.Statfs("/", &stat)
	}

	if err == nil {
		total := stat.Blocks * uint64(stat.Bsize)
		free := stat.Bfree * uint64(stat.Bsize)
		avail := stat.Bavail * uint64(stat.Bsize)
		used := total - free

		totalGB := float64(total) / (1024 * 1024 * 1024)
		usedGB := float64(used) / (1024 * 1024 * 1024)
		availGB := float64(avail) / (1024 * 1024 * 1024)
		
		var percent float64
		if total > 0 {
			percent = (float64(used) / float64(total)) * 100
		}

		info.Size = fmt.Sprintf("%.1f GB", totalGB)
		info.Used = fmt.Sprintf("%.1f GB", usedGB)
		info.Avail = fmt.Sprintf("%.1f GB", availGB)
		info.Percent = fmt.Sprintf("%.0f%%", percent)
	}

	return info
}

func getUptimeInfo() string {
	cmd := exec.Command("uptime")
	if output, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(output))
	}

	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "Unavailable"
	}

	parts := strings.Fields(string(content))
	if len(parts) > 0 {
		uptimeSecs, err := strconv.ParseFloat(parts[0], 64)
		if err == nil {
			duration := time.Duration(uptimeSecs) * time.Second
			return fmt.Sprintf("up %v", duration.Truncate(time.Second))
		}
	}

	return "Unavailable"
}

func getStaticFileHandler() http.Handler {
	wd, _ := os.Getwd()
	localIndex := filepath.Join(wd, "public", "index.html")
	if _, err := os.Stat(localIndex); err == nil {
		fmt.Println("Serving static files from local 'public/' directory (Development Mode).")
		return http.FileServer(http.Dir("public"))
	}

	fmt.Println("Serving static files from embedded asset system.")
	subFS, err := fs.Sub(embedFS, "public")
	if err != nil {
		panic("Failed to create sub filesystem from embedded: " + err.Error())
	}
	return http.FileServer(http.FS(subFS))
}

func printStartupInfo(port string) {
	fmt.Println("\n==================================================")
	fmt.Println("🔒 SECURE TERMUX SYSTEM DASHBOARD (GO SSL ENGINE)")
	fmt.Println("==================================================")
	fmt.Println("Dashboard successfully loaded in HTTPS mode with WebTTY.")
	fmt.Println("\nLocal Access:")
	fmt.Printf("   👉 https://localhost:%s\n", port)

	fmt.Println("\nNetwork Access (Wi-Fi/Tailscale):")
	ifaces, err := net.Interfaces()
	if err == nil {
		count := 0
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip == nil || ip.IsLoopback() {
					continue
				}
				ip = ip.To4()
				if ip == nil {
					continue
				}
				fmt.Printf("   👉 https://%s:%s\n", ip.String(), port)
				count++
			}
		}
		if count == 0 {
			fmt.Println("   (No active connection detected)")
		}
	} else {
		fmt.Println("   Error fetching local IP addresses.")
	}
	fmt.Println("==================================================")
	fmt.Printf("Authentication Credentials:\n")
	fmt.Printf("   Username: %s\n", basicUser)
	fmt.Printf("   Password: %s (Set DASHBOARD_PASSWORD env to override)\n", basicPass)
	fmt.Println("==================================================")
	fmt.Println("Press Ctrl+C to terminate the server.\n")
}

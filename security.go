package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// IP Access Confirmation Cache Types
type IPApprovalState struct {
	Waiters []chan bool
}

var (
	approvedIPs      = make(map[string]time.Time)
	pendingApprovals = make(map[string]*IPApprovalState)
	approvalMutex    sync.Mutex

	basicUser = "admin"
	basicPass = "" // Bcrypt hash of the password

	// Cookie Sessions & Fail2Ban
	sessions       = make(map[string]time.Time)
	sessionsMutex  sync.RWMutex
	failedAttempts = make(map[string]int)
	bannedIPs      = make(map[string]time.Time)
	securityMutex  sync.Mutex
)

func generateSessionToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func addSession(token string) {
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()
	sessions[token] = time.Now().Add(24 * time.Hour)
}

func isValidSession(token string) bool {
	sessionsMutex.RLock()
	defer sessionsMutex.RUnlock()
	expiry, exists := sessions[token]
	if !exists {
		return false
	}
	return time.Now().Before(expiry)
}

func removeSession(token string) {
	sessionsMutex.Lock()
	defer sessionsMutex.Unlock()
	delete(sessions, token)
}

func isIPBanned(ip string) bool {
	securityMutex.Lock()
	defer securityMutex.Unlock()
	expiry, exists := bannedIPs[ip]
	if !exists {
		return false
	}
	if time.Now().After(expiry) {
		delete(bannedIPs, ip)
		delete(failedAttempts, ip)
		return false
	}
	return true
}

func registerFailedAttempt(ip string) {
	securityMutex.Lock()
	defer securityMutex.Unlock()
	failedAttempts[ip]++
	if failedAttempts[ip] >= 3 {
		bannedIPs[ip] = time.Now().Add(15 * time.Minute)
	}
}

func clearFailedAttempts(ip string) {
	securityMutex.Lock()
	defer securityMutex.Unlock()
	delete(failedAttempts, ip)
}

func initAuth() {
	var plainPass string
	if u := os.Getenv("DASHBOARD_USER"); u != "" {
		basicUser = u
	}
	if p := os.Getenv("DASHBOARD_PASSWORD"); p != "" {
		plainPass = p
	} else {
		// Generate secure random password if not provided
		plainPass = "termux"
		b := make([]byte, 8)
		if _, err := rand.Read(b); err == nil {
			plainPass = fmt.Sprintf("%x", b)
			fmt.Printf("\n[SECURITY] DASHBOARD_PASSWORD tidak diatur. Membuat password acak sekali pakai:\n")
			fmt.Printf("           Password: %s\n\n", plainPass)
		}
	}

	// Copy to Android clipboard if tool exists
	if _, err := exec.LookPath("termux-clipboard-set"); err == nil {
		if err := exec.Command("termux-clipboard-set", plainPass).Run(); err == nil {
			fmt.Printf("[INFO] Password admin telah disalin ke clipboard Android.\n")
		}
	}

	// Hash password using bcrypt
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPass), bcrypt.DefaultCost)
	if err == nil {
		basicPass = string(hash)
	} else {
		fmt.Printf("[WARNING] Gagal membuat hash bcrypt, menggunakan verifikasi plain text.\n")
		basicPass = plainPass
	}
}

func extractSessionToken(r *http.Request) string {
	// 1. Try Cookie
	cookie, err := r.Cookie("session_token")
	if err == nil && cookie.Value != "" {
		return cookie.Value
	}

	// 2. Try Authorization Header
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	// 3. Try X-Session-Token Header
	if token := r.Header.Get("X-Session-Token"); token != "" {
		return token
	}

	// 4. Try Query Parameter (fallback for raw link download/navigation)
	if token := r.URL.Query().Get("session_token"); token != "" {
		return token
	}

	return ""
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

		// Fail2Ban check
		if isIPBanned(ip) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "Access Denied: Your IP is temporarily banned due to too many failed login attempts.\n")
			return
		}

		// Step A: Check Whitelist & Blacklist Access
		allowed, reason := checkIPAccess(ip)
		if !allowed {
			logBlockedAttempt(ip, r, reason)
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, "Access Denied: Your IP (%s) is not authorized. Reason: %s\n", ip, reason)
			return
		}

		// Step B: Check Session Cookie/Header (skip for /api/login and /ws/terminal)
		if r.URL.Path != "/api/login" && r.URL.Path != "/ws/terminal" {
			token := extractSessionToken(r)
			if token == "" || !isValidSession(token) {
				if strings.HasPrefix(r.URL.Path, "/api/") {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "Unauthorized"})
					return
				}
			}
		}

		// Step C: Check Interactive Android Confirmation Dialog
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

var _, cgnatNet, _ = net.ParseCIDR("100.64.0.0/10")

func isLocalNetwork(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if cgnatNet != nil && cgnatNet.Contains(ip) {
		return true
	}
	return false
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

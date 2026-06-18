package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed public/*
var embedFS embed.FS

const defaultPort = "8443"

func main() {
	initAuth()
	acquireWakeLock()

	metricsCtx, metricsCancel := context.WithCancel(context.Background())
	defer metricsCancel()
	go watchSystemMetrics(metricsCtx)
	go watchCPUUsage(metricsCtx)

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	disableTLS := os.Getenv("DISABLE_TLS") == "true"

	if !disableTLS {
		// Generate SSL certificates if missing
		if err := checkOrGenerateCert(); err != nil {
			fmt.Printf("Error checking/generating SSL certificate: %v\n", err)
			os.Exit(1)
		}
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
	mux.HandleFunc("GET /api/files/view", handleFileView)
	mux.HandleFunc("POST /api/files/save", handleFileSave)

	// Process Manager Endpoints
	mux.HandleFunc("GET /api/processes", handleProcessList)
	mux.HandleFunc("POST /api/processes/kill", handleProcessKill)

	// WebSocket / Token Endpoints
	mux.HandleFunc("POST /api/token", handleGetToken)
	mux.HandleFunc("GET /ws/terminal", handleTerminalWS)

	// Static file handler (with local folder fallback)
	mux.Handle("/", getStaticFileHandler())

	// Start HTTPS server wrapped with connection logger, IP access, and Basic Auth middleware
	serverAddr := ":" + port
	server := &http.Server{
		Addr:    serverAddr,
		Handler: loggingMiddleware(mux),
	}

	// Channel to listen for interrupt/termination signals
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		printStartupInfo(port)
		var err error
		if disableTLS {
			err = server.ListenAndServe()
		} else {
			err = server.ListenAndServeTLS("cert.pem", "key.pem")
		}
		if err != nil && err != http.ErrServerClosed {
			fmt.Printf("Error starting server: %v\n", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	<-stopChan
	fmt.Println("\n[INFO] Sinyal penghentian diterima. Membersihkan koneksi...")
	releaseWakeLock()

	// Graceful shutdown context with 5s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		fmt.Printf("Error during server shutdown: %v\n", err)
	}

	fmt.Println("[INFO] Server berhenti secara aman. Selesai.")
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

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func getLocalIPs() []string {
	var ips []string
	if primary := getOutboundIP(); primary != "" {
		ips = append(ips, primary)
	}

	ifaces, err := net.Interfaces()
	if err == nil {
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

				exists := false
				for _, existing := range ips {
					if existing == ip.String() {
						exists = true
						break
					}
				}
				if !exists {
					ips = append(ips, ip.String())
				}
			}
		}
	}
	return ips
}

func printStartupInfo(port string) {
	disableTLS := os.Getenv("DISABLE_TLS") == "true"
	scheme := "https"
	modeText := "HTTPS mode with WebTTY"
	lockEmoji := "🔒"
	titleText := "SECURE TERMUX SYSTEM DASHBOARD (GO SSL ENGINE)"
	if disableTLS {
		scheme = "http"
		modeText = "HTTP mode (TLS Disabled) with WebTTY"
		lockEmoji = "🔓"
		titleText = "TERMUX SYSTEM DASHBOARD (TLS DISABLED)"
	}

	fmt.Println("\n==================================================")
	fmt.Printf("%s %s\n", lockEmoji, titleText)
	fmt.Println("==================================================")
	fmt.Printf("Dashboard successfully loaded in %s.\n", modeText)
	fmt.Println("\nLocal Access:")
	fmt.Printf("   👉 %s://localhost:%s\n", scheme, port)

	fmt.Println("\nNetwork Access (Wi-Fi/Tailscale):")
	ips := getLocalIPs()
	if len(ips) > 0 {
		for _, ip := range ips {
			fmt.Printf("   👉 %s://%s:%s\n", scheme, ip, port)
		}
	} else {
		fmt.Println("   (No active connection detected)")
	}
	fmt.Println("==================================================")
	fmt.Printf("Authentication Credentials:\n")
	fmt.Printf("   Username: %s\n", basicUser)
	fmt.Printf("   Password: %s (Set DASHBOARD_PASSWORD env to override)\n", "[HIDDEN] (Bcrypt Protected)")
	fmt.Println("==================================================")
	fmt.Println("Press Ctrl+C to terminate the server.")
}

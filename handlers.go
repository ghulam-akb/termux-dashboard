package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Request/Response structures
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

type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mtime"`
}

type FileDeletePayload struct {
	Path string `json:"path"`
}

type FileViewResponse struct {
	Content string `json:"content"`
}

type FileSavePayload struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ProcessKillPayload struct {
	PID int `json:"pid"`
}

// Stats Handler
func handleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	rx, tx := getNetworkSpeeds()
	stats := StatsResponse{
		Battery: getBatteryInfo(),
		RAM:     getRAMInfo(),
		Storage: getStorageInfo(),
		Uptime:  getUptimeInfo(),
		CPU:     getCPUInfo(),
		System:  getSystemInfo(),
		NetRX:   rx,
		NetTX:   tx,
	}

	json.NewEncoder(w).Encode(stats)
}

// Diagnostics Handlers
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

// File Manager Handlers
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

// Built-in File Editor Handlers
func handleFileView(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	path := r.URL.Query().Get("path")
	if path == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Path parameter missing"})
		return
	}
	path = filepath.Clean(path)

	content, err := os.ReadFile(path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to read file: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(FileViewResponse{Content: string(content)})
}

func handleFileSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	r.Body = http.MaxBytesReader(w, r.Body, 10*1024*1024) // Limit to 10MB
	var payload FileSavePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid payload"})
		return
	}

	path := filepath.Clean(payload.Path)
	if path == "/" || path == "/data" || path == "/data/data" || path == "/data/data/com.termux" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Modifying critical system directories is forbidden"})
		return
	}

	err := os.WriteFile(path, []byte(payload.Content), 0644)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to write file: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Saved"})
}

// Process Manager Handlers
func handleProcessList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	list, err := getProcessList()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to get processes: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(list)
}

func handleProcessKill(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var payload ProcessKillPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid payload"})
		return
	}

	if payload.PID <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid PID"})
		return
	}

	err := killProcess(payload.PID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to kill process: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Killed"})
}

// PAYLOADS FOR LOGIN/LOGOUT & FILE ACTIONS
type LoginPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type FileCreatePayload struct {
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type FileRenamePayload struct {
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}

// HANDLERS FOR LOGIN/LOGOUT
func handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip = strings.Split(xff, ",")[0]
	}

	if isIPBanned(ip) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Too many failed attempts. Banned for 15 minutes."})
		return
	}

	if !isLocalNetwork(ip) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Login using local credentials from a public/external network is blocked."})
		return
	}

	var payload LoginPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid payload"})
		return
	}

	isValid := false
	if payload.Username == basicUser {
		err := bcrypt.CompareHashAndPassword([]byte(basicPass), []byte(payload.Password))
		if err == nil {
			isValid = true
		} else if basicPass == payload.Password {
			isValid = true
		}
	}

	if !isValid {
		registerFailedAttempt(ip)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid username or password"})
		return
	}

	clearFailedAttempts(ip)

	token := generateSessionToken()
	addSession(token)

	disableTLS := os.Getenv("DISABLE_TLS") == "true"
	cookie := &http.Cookie{
		Name:     "session_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   !disableTLS,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	}
	http.SetCookie(w, cookie)

	json.NewEncoder(w).Encode(map[string]string{
		"status": "success",
		"token":  token,
	})
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	token := extractSessionToken(r)
	if token != "" {
		removeSession(token)
	}

	delCookie := &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
	http.SetCookie(w, delCookie)

	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

// HANDLERS FOR FILE MANAGER CREATION & RENAMING
func handleFileCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var payload FileCreatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid payload"})
		return
	}

	path := filepath.Clean(payload.Path)
	if path == "/" || path == "/data" || path == "/data/data" || path == "/data/data/com.termux" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Creating files in critical system directories is forbidden"})
		return
	}

	var err error
	if payload.IsDir {
		err = os.MkdirAll(path, 0755)
	} else {
		err = os.WriteFile(path, []byte(""), 0644)
	}

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to create: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Created"})
}

func handleFileRename(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var payload FileRenamePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid payload"})
		return
	}

	oldPath := filepath.Clean(payload.OldPath)
	newPath := filepath.Clean(payload.NewPath)

	if oldPath == "/" || oldPath == "/data" || oldPath == "/data/data" || oldPath == "/data/data/com.termux" ||
		newPath == "/" || newPath == "/data" || newPath == "/data/data" || newPath == "/data/data/com.termux" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"error": "Modifying critical system directories is forbidden"})
		return
	}

	err := os.Rename(oldPath, newPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "Failed to rename/move: " + err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "Renamed"})
}

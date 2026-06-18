package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var (
	wsTokens      = make(map[string]time.Time)
	wsTokensMutex sync.Mutex
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func generateWSToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	token := fmt.Sprintf("%x", b)

	wsTokensMutex.Lock()
	wsTokens[token] = time.Now().Add(15 * time.Second) // Valid for 15s
	wsTokensMutex.Unlock()

	return token
}

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

	// Mutex to protect concurrent writes to the WebSocket connection
	var writeMutex sync.Mutex
	writeMessage := func(messageType int, data []byte) error {
		writeMutex.Lock()
		defer writeMutex.Unlock()
		return conn.WriteMessage(messageType, data)
	}

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

	// Write welcoming banner
	banner := fmt.Sprintf("\r\n\x1b[1;36m==================================================\x1b[0m\r"+
		"\n\x1b[1;32m   Welcome to Secure Termux WebTTY Terminal      \x1b[0m\r"+
		"\n\x1b[1;36m==================================================\x1b[0m\r"+
		"\n   Device:  Android Termux\r"+
		"\n   Shell:   %s\r"+
		"\n   Time:    %s\r"+
		"\n\x1b[1;30mType 'exit' to close this connection.\x1b[0m\r"+
		"\n\x1b[1;36m==================================================\x1b[0m\r\n\r\n", shell, time.Now().Format("2006-01-02 15:04:05"))
	writeMessage(websocket.BinaryMessage, []byte(banner))

	// Setup read limit and deadline
	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Start ping ticker to keep WebSocket connection alive
	pingTicker := time.NewTicker(25 * time.Second)
	defer pingTicker.Stop()

	// Done channel to stop the ping goroutine when the connection ends
	doneChan := make(chan struct{})
	defer close(doneChan)

	go func() {
		for {
			select {
			case <-pingTicker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := writeMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-doneChan:
				return
			}
		}
	}()

	// Pipe PTY stdout/stderr -> WebSocket
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := f.Read(buf)
			if err != nil {
				writeMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := writeMessage(websocket.BinaryMessage, buf[:n]); err != nil {
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
		// Reset read deadline on every message received
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
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

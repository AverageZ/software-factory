package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The TCP endpoint only discovers the control socket. Restart and the launch
// environment are available only through the owner-only Unix socket.
type daemonInfo struct {
	Protocol string `json:"protocol"`
	Instance string `json:"instance"`
	PID      int    `json:"pid"`
	Address  string `json:"address"`
	Data     string `json:"data"`
	Web      string `json:"web"`
	Control  string `json:"control"`
}

type daemonLaunch struct {
	daemonInfo
	Args        []string `json:"args"`
	Directory   string   `json:"directory"`
	Environment []string `json:"environment"`
}

type restartControl struct {
	info     daemonInfo
	listener net.Listener
	server   *http.Server
}

func lifecycleJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func startRestartControl(address, data, web string, args []string, prepare func() error, stop func()) (*restartControl, error) {
	if strings.HasPrefix(data, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		data = filepath.Join(home, data[2:])
	}
	data, err := filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	web, err = filepath.Abs(web)
	if err != nil {
		return nil, err
	}
	directory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// A private, short path also works with macOS's Unix socket path limit.
	controlDirectory, err := os.MkdirTemp("/tmp", "factoryd-")
	if err != nil {
		return nil, err
	}
	controlPath := filepath.Join(controlDirectory, "control.sock")
	listener, err := net.Listen("unix", controlPath)
	if err != nil {
		_ = os.Remove(controlDirectory)
		return nil, fmt.Errorf("open restart control socket: %w", err)
	}
	if err := os.Chmod(controlPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(controlDirectory)
		return nil, err
	}
	info := daemonInfo{"factoryd-restart-v1", rand.Text(), os.Getpid(), address, data, web, controlPath}
	launch := daemonLaunch{info, append([]string{}, args...), directory, os.Environ()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		lifecycleJSON(w, http.StatusOK, launch)
	})
	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Factory-Instance") != info.Instance {
			lifecycleJSON(w, http.StatusConflict, map[string]string{"error": "daemon instance changed"})
			return
		}
		if err := prepare(); err != nil {
			lifecycleJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		lifecycleJSON(w, http.StatusOK, map[string]string{"status": "stopping"})
		stop()
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	control := &restartControl{info, listener, server}
	go func() { _ = server.Serve(listener) }()
	return control, nil
}

func (c *restartControl) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/daemon" {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "daemon control discovery is local only", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		lifecycleJSON(w, http.StatusOK, c.info)
	})
}

func (c *restartControl) close() error {
	err := c.server.Close()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, os.RemoveAll(filepath.Dir(c.info.Control)))
}

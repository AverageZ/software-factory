package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestRestartControlPreservesLaunchAndRejectsBusy(t *testing.T) {
	var stopped atomic.Bool
	var busy atomic.Bool
	busy.Store(true)
	args := []string{"-data", "custom data", "-web", "custom web", "-addr", "127.0.0.1:9090"}
	control, err := startRestartControl("127.0.0.1:9090", t.TempDir(), "custom web", args, func() error {
		if busy.Load() {
			return errors.New("active managed work")
		}
		return nil
	}, func() { stopped.Store(true) })
	if err != nil {
		t.Fatal(err)
	}
	defer control.close()
	metadata, err := os.Stat(control.info.Control)
	if err != nil || metadata.Mode().Perm() != 0600 {
		t.Fatalf("control socket is not private: %v %v", metadata, err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", control.info.Control)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Get("http://localhost/info")
	if err != nil {
		t.Fatal(err)
	}
	var launch daemonLaunch
	err = json.NewDecoder(response.Body).Decode(&launch)
	response.Body.Close()
	if err != nil || launch.PID != os.Getpid() || launch.Address != "127.0.0.1:9090" || len(launch.Args) != len(args) || len(launch.Environment) == 0 {
		t.Fatalf("launch configuration lost: %+v, %v", launch, err)
	}
	for index, arg := range args {
		if launch.Args[index] != arg {
			t.Fatalf("argument changed: %q != %q", launch.Args[index], arg)
		}
	}
	cwd, err := os.Getwd()
	if err != nil || launch.Directory != cwd || !filepath.IsAbs(launch.Data) || !filepath.IsAbs(launch.Web) {
		t.Fatalf("launch paths lost: %+v, %v", launch, err)
	}
	for _, test := range []struct {
		name     string
		instance string
		busy     bool
		status   int
	}{
		{"stale instance", "another instance", false, http.StatusConflict},
		{"active managed work", control.info.Instance, true, http.StatusConflict},
		{"idle daemon", control.info.Instance, false, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			busy.Store(test.busy)
			request, err := http.NewRequest(http.MethodPost, "http://localhost/restart", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("X-Factory-Instance", test.instance)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != test.status || stopped.Load() != (test.status == http.StatusOK) {
				t.Fatalf("status=%d stopped=%v", response.StatusCode, stopped.Load())
			}
		})
	}
}

func TestDaemonDiscoveryIsLocalAndDoesNotExposeEnvironment(t *testing.T) {
	control := &restartControl{info: daemonInfo{Protocol: "factoryd-restart-v1", PID: os.Getpid()}}
	handler := control.handler(http.NotFoundHandler())
	for _, test := range []struct {
		method string
		remote string
		status int
	}{
		{http.MethodGet, "127.0.0.1:3000", http.StatusOK},
		{http.MethodGet, "[::1]:3000", http.StatusOK},
		{http.MethodGet, "192.0.2.1:3000", http.StatusForbidden},
		{http.MethodPost, "127.0.0.1:3000", http.StatusMethodNotAllowed},
	} {
		request := httptest.NewRequest(test.method, "/api/daemon", nil)
		request.RemoteAddr = test.remote
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("%s %s: status %d", test.method, test.remote, response.Code)
		}
		if response.Code == http.StatusOK {
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if _, exists := body["environment"]; exists {
				t.Fatal("TCP discovery exposed the launch environment")
			}
		}
	}
}

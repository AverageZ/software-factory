package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	} // The peer may disconnect after headers.
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var api *APIError
	if errors.As(err, &api) {
		status = api.Status
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalid(fmt.Errorf("invalid JSON: %w", err))
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid(fmt.Errorf("request must contain exactly one JSON object"))
	}
	return nil
}
func localhost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
}

// ListenAddress requires explicit consent before opening the local execution API
// on a non-loopback interface. This API intentionally has no remote authentication.
func ListenAddress(address string, allowPublic bool) (string, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("invalid listen address: %w", err)
	}
	if !allowPublic && !localhost(host) {
		return "", fmt.Errorf("refusing non-loopback address %q; -allow-public explicitly exposes local code execution without authentication", address)
	}
	return address, nil
}
func (e *Engine) Handler(web string, allowPublic bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		err := e.healthyLocked()
		e.mu.Unlock()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, e.Projects()) })
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var p Project
		if err := decodeJSON(w, r, &p); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.SaveProject(p)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/projects/{projectId}/repositories/{repoId}/github", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.repositoryGitHub(r.PathValue("projectId"), r.PathValue("repoId"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /api/projects/{projectId}/repositories/{repoId}/github/check", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.updateRepositoryGitHub(r.Context(), r.PathValue("projectId"), r.PathValue("repoId"), nil)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /api/projects/{projectId}/repositories/{repoId}/github/labels", func(w http.ResponseWriter, r *http.Request) {
		var target GitHubTarget
		if err := decodeJSON(w, r, &target); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.updateRepositoryGitHub(r.Context(), r.PathValue("projectId"), r.PathValue("repoId"), &target)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/projects/{projectId}/repositories/{repoId}/intake", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.repositoryIntake(r.PathValue("projectId"), r.PathValue("repoId"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("PUT /api/projects/{projectId}/repositories/{repoId}/intake", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		if body.Enabled == nil {
			writeError(w, invalid(fmt.Errorf("enabled must be a boolean")))
			return
		}
		result, err := e.setRepositoryIntake(r.PathValue("projectId"), r.PathValue("repoId"), *body.Enabled)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("PUT /api/projects/{projectId}/repositories/{repoId}/intake/review", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BindingID string `json:"bindingId"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.setReviewBinding(r.PathValue("projectId"), r.PathValue("repoId"), body.BindingID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /api/projects/{projectId}/repositories/{repoId}/intake/poll", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.pollRepositoryIntake(r.Context(), r.PathValue("projectId"), r.PathValue("repoId"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, e.Workflows()) })
	mux.HandleFunc("POST /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		var workflow Workflow
		if err := decodeJSON(w, r, &workflow); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.SaveWorkflow(workflow)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/workflows/{id}/layout", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.Layout(r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("PUT /api/workflows/{id}/layout", func(w http.ResponseWriter, r *http.Request) {
		var layout Layout
		if err := decodeJSON(w, r, &layout); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.SaveLayout(r.PathValue("id"), layout)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/bindings", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, e.Bindings()) })
	mux.HandleFunc("POST /api/bindings", func(w http.ResponseWriter, r *http.Request) {
		var binding Binding
		if err := decodeJSON(w, r, &binding); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.SaveBinding(binding)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, e.Runs(r.URL.Query().Get("projectId")))
	})
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			BindingID string           `json:"bindingId"`
			Inputs    map[string]Value `json:"inputs"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.Start(body.BindingID, body.Inputs)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, result)
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.Run(r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /api/runs/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NodeID   string `json:"nodeId"`
			Approved *bool  `json:"approved"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		if body.Approved == nil {
			writeError(w, invalid(fmt.Errorf("approved must be a boolean")))
			return
		}
		result, err := e.Approve(r.PathValue("id"), body.NodeID, *body.Approved)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /api/runs/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			NodeID string `json:"nodeId"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, err)
			return
		}
		result, err := e.Retry(r.PathValue("id"), body.NodeID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /api/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		result, err := e.Cancel(r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /api/runs/{id}/nodes/{nodeId}/log", func(w http.ResponseWriter, r *http.Request) {
		filename, err := e.LogFile(r.PathValue("id"), r.PathValue("nodeId"), r.URL.Query().Get("attempt"))
		if err != nil {
			writeError(w, err)
			return
		}
		serveSavedFile(w, r, filename, true)
	})
	mux.HandleFunc("GET /api/runs/{id}/nodes/{nodeId}/artifacts/{name}", func(w http.ResponseWriter, r *http.Request) {
		filename, err := e.ArtifactFile(r.PathValue("id"), r.PathValue("nodeId"), r.PathValue("name"))
		if err != nil {
			writeError(w, err)
			return
		}
		serveSavedFile(w, r, filename, false)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeError(w, missing("API endpoint")) })
	mux.HandleFunc("/", spa(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		host := r.Host
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			host = parsed
		}
		if !allowPublic && !localhost(host) {
			writeError(w, &APIError{403, "request Host must be localhost or a loopback address"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				if err != nil || u.Scheme != scheme || !strings.EqualFold(u.Host, r.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
					writeError(w, &APIError{403, "foreign Origin is not allowed"})
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, &APIError{403, "cross-site mutations are not allowed"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func serveSavedFile(w http.ResponseWriter, r *http.Request, filename string, plain bool) {
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, missing("saved file"))
		} else {
			writeError(w, err)
		}
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, err)
		return
	}
	if plain {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
func spa(web string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, &APIError{405, "method not allowed"})
			return
		}
		if r.URL.Path == "/api" {
			writeError(w, missing("API endpoint"))
			return
		}
		root, err := os.OpenRoot(web)
		if err != nil {
			http.Error(w, "Factory web build is unavailable. Build web/dist or pass -web.", 503)
			return
		}
		defer root.Close()
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		file, err := root.Open(name)
		if err == nil {
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() {
				file.Close()
				file = nil
				err = os.ErrNotExist
			}
		}
		if err != nil {
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
			file, err = root.Open("index.html")
			if err != nil {
				http.Error(w, "Factory web build is unavailable.", 503)
				return
			}
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	}
}

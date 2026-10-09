// factoryctl is a small client of the same daemon API used by the UI.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	base := flag.String("url", "http://127.0.0.1:8080", "Factory daemon URL")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), `Usage: factoryctl [-url URL] COMMAND
  projects | workflows | bindings | runs     List resources
  run ID                                    Inspect a run
  start BINDING_ID                           Start a real workflow
  approve RUN_ID NODE_ID | reject RUN_ID NODE_ID
  retry RUN_ID NODE_ID | cancel RUN_ID
  log RUN_ID NODE_ID                         Read real process output
  api METHOD /api/PATH [JSON_FILE|-]          Call any API; '-' reads stdin

Create/update projects, workflows and bindings with 'api POST /api/RESOURCE file.json'.
Factory never installs repository rules or skills. All executions are real.`)
	}
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		return errors.New("a command is required")
	}
	method, path := http.MethodGet, ""
	var body []byte
	marshal := func(v any) { body, _ = json.Marshal(v) }
	switch args[0] {
	case "projects", "workflows", "bindings", "runs":
		if len(args) != 1 {
			return errors.New("list commands take no arguments")
		}
		path = "/api/" + args[0]
	case "run":
		if len(args) != 2 {
			return errors.New("usage: run ID")
		}
		path = "/api/runs/" + url.PathEscape(args[1])
	case "start":
		if len(args) != 2 {
			return errors.New("usage: start BINDING_ID")
		}
		method, path = http.MethodPost, "/api/runs"
		marshal(map[string]string{"bindingId": args[1]})
	case "approve", "reject", "retry":
		if len(args) != 3 {
			return errors.New("usage: approve|reject|retry RUN_ID NODE_ID")
		}
		action := args[0]
		if action == "reject" {
			action = "approve"
		}
		method, path = http.MethodPost, "/api/runs/"+url.PathEscape(args[1])+"/"+action
		marshal(map[string]any{"nodeId": args[2], "approved": args[0] == "approve"})
	case "cancel":
		if len(args) != 2 {
			return errors.New("usage: cancel RUN_ID")
		}
		method, path, body = http.MethodPost, "/api/runs/"+url.PathEscape(args[1])+"/cancel", []byte(`{}`)
	case "log":
		if len(args) != 3 {
			return errors.New("usage: log RUN_ID NODE_ID")
		}
		path = "/api/runs/" + url.PathEscape(args[1]) + "/nodes/" + url.PathEscape(args[2]) + "/log"
	case "api":
		if len(args) < 3 || len(args) > 4 {
			return errors.New("usage: api METHOD /api/PATH [JSON_FILE|-]")
		}
		method, path = strings.ToUpper(args[1]), args[2]
		if !strings.HasPrefix(path, "/api/") || strings.Contains(path, "..") {
			return errors.New("path must be beneath /api/")
		}
		if len(args) == 4 {
			var err error
			if args[3] == "-" {
				body, err = io.ReadAll(os.Stdin)
			} else {
				body, err = os.ReadFile(args[3])
			}
			if err != nil {
				return err
			}
			if !json.Valid(body) {
				return errors.New("request body is not valid JSON")
			}
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	req, err := http.NewRequest(method, strings.TrimRight(*base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}

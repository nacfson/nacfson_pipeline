package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

type recordedRequest struct {
	Path    string          `json:"path"`
	Channel string          `json:"channel"`
	Payload json.RawMessage `json:"payload"`
}

func main() {
	var mu sync.Mutex
	var recorded []recordedRequest

	http.HandleFunc("/v1/notify/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		trimmed := bytes.TrimSpace(body)
		var compact bytes.Buffer
		if len(trimmed) > 0 && json.Compact(&compact, trimmed) == nil {
			trimmed = compact.Bytes()
		}
		fmt.Fprintf(os.Stdout, "%s\n", trimmed)

		payload := json.RawMessage(append([]byte(nil), trimmed...))
		if !json.Valid(payload) {
			if encoded, mErr := json.Marshal(string(trimmed)); mErr == nil {
				payload = encoded
			} else {
				payload = json.RawMessage("null")
			}
		}

		channel := strings.TrimPrefix(r.URL.Path, "/v1/notify/")
		mu.Lock()
		recorded = append(recorded, recordedRequest{
			Path:    r.URL.Path,
			Channel: channel,
			Payload: payload,
		})
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})

	http.HandleFunc("/requests", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		out := append([]recordedRequest(nil), recorded...)
		mu.Unlock()
		if out == nil {
			out = []recordedRequest{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

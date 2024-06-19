package cmd

import (
	"io"
	"net/http"
	"strings"
	"fmt"
)

func handleAnthropicProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/anthropic")
	if path != "/v1/messages" {
		http.Error(w, "Unsupported Anthropic API endpoint", http.StatusBadRequest)
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, fmt.Sprintf("https://api.anthropic.com%s", path), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("X-API-Key", anthropicAPIKey)
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	proxyReq.Header.Set("anthropic-version", "2023-06-01")
	proxyReq.Header.Set("anthropic-beta", "tools-2024-04-04")

	client := &http.Client{}
	proxyResp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer proxyResp.Body.Close()

	copyHeaders(w.Header(), proxyResp.Header)
	w.WriteHeader(proxyResp.StatusCode)
	io.Copy(w, proxyResp.Body)
}

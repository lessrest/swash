package cmd

import (
	"io"
	"net/http"
	"strings"
	"fmt"
)

func handleOpenAIProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/openai")
	allowedPaths := []string{
		"/v1/audio/transcriptions",
		"/v1/chat/completions",
		"/v1/audio/translations",
		"/v1/embeddings",
		"/v1/images/generations",
	}
	if !contains(allowedPaths, path) {
		http.Error(w, "Unsupported OpenAI API endpoint", http.StatusBadRequest)
		return
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, fmt.Sprintf("https://api.openai.com%s", path), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	proxyReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", openaiAPIKey))
	proxyReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))

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

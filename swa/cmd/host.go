package cmd

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	openaiAPIKey    = os.Getenv("OPENAI_API_KEY")
	anthropicAPIKey = os.Getenv("ANTHROPIC_API_KEY")
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
	log.Printf("is an HTTP request handling process")
	log.Printf("began on %s", time.Now())
	log.Printf("has method %s", r.Method)
	log.Printf("has pathname %s", r.URL.Path)
	log.Printf("has origin [redacted]")

	defer func() {
		if err := recover(); err != nil {
			log.Printf("failed at %s", time.Now())
			log.Printf("has error %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()

	switch {
	case r.URL.Path == "/transcribe":
		handleTranscribe(w, r)
	case strings.HasPrefix(r.URL.Path, "/whisper"):
		handleWhisperDeepgram(w, r)
	case strings.HasPrefix(r.URL.Path, "/openai/"):
		handleOpenAIProxy(w, r)
	case strings.HasPrefix(r.URL.Path, "/anthropic/"):
		handleAnthropicProxy(w, r)
	case strings.HasPrefix(r.URL.Path, "/whisper"):
		handleWhisperDeepgram(w, r)

	default:
		http.NotFound(w, r)
	}
}

func Serve() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	hostname := os.Getenv("HOST")

	log.Printf("Starting server on %s:%s", hostname, port)

	http.HandleFunc("/", handleRequest)

	if err := http.ListenAndServe(fmt.Sprintf("%s:%s", hostname, port), nil); err != nil {
		log.Fatal(err)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

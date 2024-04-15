package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{} // use default options
var deepgramApiKey = getEnv("DEEPGRAM_API_KEY")
var openaiApiKey = getEnv("OPENAI_API_KEY")
var anthropicApiKey = getEnv("ANTHROPIC_API_KEY")

func deepgramUrl(language string) string {
	queryParams := url.Values{
		"model":           {"nova-2"},
		"interim_results": {"true"},
		"smart_format":    {"true"},
		"vad_events":      {"true"},
		"diarize":         {"true"},
		"language":        {language},
	}
	return "wss://api.deepgram.com/v1/listen?" + queryParams.Encode()
}

func getEnv(name string) string {
	value, exists := os.LookupEnv(name)
	if !exists {
		log.Fatalf("Missing environment variable: %s", name)
	}
	return value
}

func startProxySession(serviceConn *websocket.Conn, browserConn *websocket.Conn) {
	log.Info("session", "from", browserConn.RemoteAddr())

	go func() {
		defer serviceConn.Close()
		defer browserConn.Close()
		for {
			messageType, message, err := browserConn.ReadMessage()
			if err != nil {
				log.Error("read from browser", "error", err)
				break
			}

			if err := serviceConn.WriteMessage(messageType, message); err != nil {
				log.Error("send to Deepgram", "error", err)
				break
			}
		}
	}()

	go func() {
		defer serviceConn.Close()
		defer browserConn.Close()
		for {
			messageType, message, err := serviceConn.ReadMessage()
			if err != nil {
				log.Error("read from Deepgram", "error", err)
				break
			}
			if err := browserConn.WriteMessage(messageType, message); err != nil {
				log.Error("send to browser", "error", err)
				break
			}
		}
	}()

	// Wait for either connection to close
	select {}
}

func handleTranscribe(w http.ResponseWriter, r *http.Request) {
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("upgrade", "error", err)
		return
	}
	defer browserConn.Close()

	language := r.URL.Query().Get("language")
	if language == "" {
		language = "en-US"
	}

	url := deepgramUrl(language)
	header := http.Header{"Authorization": {"Token " + deepgramApiKey}}
	serviceConn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		log.Error("dial", "error", err)
		return
	}

	startProxySession(serviceConn, browserConn)
	log.Info("session ended", "from", browserConn.RemoteAddr())
}

func whisper(w http.ResponseWriter, r *http.Request) {
	prefix := r.FormValue("prefix")

	// Read the audio file from the request
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Error("Error retrieving the file", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create a new multipart writer
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create a new form file field and copy the uploaded file data to it
	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		log.Error("Error creating form file", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(part, file)
	if err != nil {
		log.Error("Error copying file data", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Add the model field
	_ = writer.WriteField("model", "whisper-1")

	// Add the response format field
	_ = writer.WriteField("response_format", "verbose_json")

	_ = writer.WriteField("timestamp_granularities[]", "word")
	_ = writer.WriteField("timestamp_granularities[]", "segment")

	// Add the prefix field
	_ = writer.WriteField("prompt", prefix)

	// Close the multipart writer to finalize the request body
	err = writer.Close()
	if err != nil {
		log.Error("Error closing multipart writer", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := sendOpenAIRequest("POST", "/v1/audio/transcriptions", writer.FormDataContentType(), body)
	if err != nil {
		handleError(w, err)
		return
	}
	defer resp.Body.Close()

	handleResponse(w, resp)
}

func whisperDeepgram(w http.ResponseWriter, r *http.Request) {
	// Read the audio file from the request
	file, _, err := r.FormFile("file")
	if err != nil {
		log.Error("Error retrieving the file", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// get the language from the request
	language := r.URL.Query().Get("language")
	if language == "" {
		language = "en-US"
	}

	// Read the audio file content
	audioData, err := io.ReadAll(file)
	if err != nil {
		log.Error("Error reading audio file", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := sendDeepgramRequest(audioData, language)
	if err != nil {
		handleError(w, err)
		return
	}
	defer resp.Body.Close()

	handleResponse(w, resp)
}

func handleOpenAIProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/openai")
	if path != "/v1/audio/transcriptions" && path != "/v1/chat/completions" {
		log.Error("Unsupported OpenAI API endpoint", "path", path)
		http.Error(w, "Unsupported OpenAI API endpoint", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequest(r.Method, "https://api.openai.com"+path, r.Body)
	if err != nil {
		log.Error("Error creating request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	req.Header.Set("Authorization", "Bearer "+openaiApiKey)
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w, resp)
	buf := make([]byte, 16) // Use a small buffer size for streaming
	for {
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			log.Error("Error reading response body", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			break
		}
		if n == 0 {
			break
		}
		if _, err := w.Write(buf[:n]); err != nil {
			log.Error("Error writing response body", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			break
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // Flush the response writer if it supports flushing
		}
	}
}

func copyHeaders(w http.ResponseWriter, resp *http.Response) {
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}

func main() {
	port := getEnv("PORT")

	http.Handle("/transcribe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleTranscribe(w, r)
	}))
	http.Handle("/whisper", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		whisper(w, r)
	}))
	http.Handle("/whisper-deepgram", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		whisperDeepgram(w, r)
	}))
	http.Handle("/openai/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleOpenAIProxy(w, r)
	}))
	http.Handle("/anthropic/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleAnthropicProxy(w, r)
	}))

	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	log.Info("serve", "port", port)

	log.Fatal(http.ListenAndServe(":"+port, nil))
}
func sendOpenAIRequest(method, path, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "https://api.openai.com"+path, body)
	if err != nil {
		log.Error("Error creating request", "error", err)
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+openaiApiKey)
	req.Header.Set("Content-Type", contentType)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		return nil, err
	}

	return resp, nil
}

func sendDeepgramRequest(audioData []byte, language string) (*http.Response, error) {
	req, err := http.NewRequest("POST", "https://api.deepgram.com/v1/listen", bytes.NewBuffer(audioData))
	if err != nil {
		log.Error("Error creating request", "error", err)
		return nil, err
	}

	req.Header.Set("Authorization", "Token "+deepgramApiKey)
	req.Header.Set("Content-Type", "audio/webm")

	q := req.URL.Query()
	q.Set("model", "nova-2")
	q.Set("diarize", "true")
	q.Set("smart_format", "true")
	q.Set("language", language)
	req.URL.RawQuery = q.Encode()

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		return nil, err
	}

	return resp, nil
}

func handleError(w http.ResponseWriter, err error) {
	log.Error("Error occurred", "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func handleResponse(w http.ResponseWriter, resp *http.Response) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		handleError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(respBody)
	if err != nil {
		handleError(w, err)
	}
}
func handleAnthropicProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/anthropic")
	if path != "/v1/messages" {
		log.Error("Unsupported Anthropic API endpoint", "path", path)
		http.Error(w, "Unsupported Anthropic API endpoint", http.StatusBadRequest)
		return
	}

	req, err := http.NewRequest(r.Method, "https://api.anthropic.com"+path, r.Body)
	if err != nil {
		log.Error("Error creating request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	req.Header.Set("X-API-Key", anthropicApiKey)
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	//anthropic-version: 2023-06-01
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w, resp)
	buf := make([]byte, 16) // Use a small buffer size for streaming
	for {
		n, err := resp.Body.Read(buf)
		if err != nil && err != io.EOF {
			log.Error("Error reading response body", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			break
		}
		if n == 0 {
			break
		}
		if _, err := w.Write(buf[:n]); err != nil {
			log.Error("Error writing response body", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			break
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // Flush the response writer if it supports flushing
		}
	}
}
func sendAnthropicRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, "https://api.anthropic.com"+path, body)
	if err != nil {
		log.Error("Error creating request", "error", err)
		return nil, err
	}

	req.Header.Set("X-API-Key", anthropicApiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		return nil, err
	}

	return resp, nil
}

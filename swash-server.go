package main

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{} // use default options
var deepgramApiKey = getEnv("DEEPGRAM_API_KEY")
var openaiApiKey = getEnv("OPENAI_API_KEY")

var deepgramQueryParams = map[string]string{
	"model":           "nova-2",
	"interim_results": "true",
	"smart_format":    "true",
	"vad_events":      "true",
	"diarize":         "true",
	"language":        "en-US",
}

func deepgramUrl() string {
	queryParams := url.Values{}
	for key, value := range deepgramQueryParams {
		queryParams.Add(key, value)
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

func handler(w http.ResponseWriter, r *http.Request) {
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error("upgrade", "error", err)
		return
	}

	defer browserConn.Close()

	url := deepgramUrl()
	header := http.Header{"Authorization": {"Token " + deepgramApiKey}}
	serviceConn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		log.Error("dial", "error", err)
		return
	}

	startProxySession(serviceConn, browserConn)
	log.Info("over", "from", browserConn.RemoteAddr())
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

	// Create a new HTTP request
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", body)
	if err != nil {
		log.Error("Error creating request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set the request headers
	req.Header.Set("Authorization", "Bearer "+openaiApiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Error reading response body", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set the response headers
	w.Header().Set("Content-Type", "application/json")

	// Write the response body
	_, err = w.Write(respBody)
	if err != nil {
		log.Error("Error writing response", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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

	// Read the audio file content
	audioData, err := io.ReadAll(file)
	if err != nil {
		log.Error("Error reading audio file", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create a new HTTP request
	req, err := http.NewRequest("POST", "https://api.deepgram.com/v1/listen", bytes.NewBuffer(audioData))
	if err != nil {
		log.Error("Error creating request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set the request headers
	req.Header.Set("Authorization", "Token "+deepgramApiKey)
	req.Header.Set("Content-Type", "audio/webm")

	// Set the query parameters
	q := req.URL.Query()
	q.Set("model", "nova-2")
	q.Set("diarize", "true")
	q.Set("smart_format", "true")
	q.Set("language", "en-US")
	req.URL.RawQuery = q.Encode()

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Error("Error sending request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Read the response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error("Error reading response body", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set the response headers
	w.Header().Set("Content-Type", "application/json")

	// Write the response body
	_, err = w.Write(respBody)
	if err != nil {
		log.Error("Error writing response", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func main() {
	port := getEnv("PORT")

	http.HandleFunc("/transcribe", handler)
	http.HandleFunc("/whisper", whisper)
	http.HandleFunc("/whisper-deepgram", whisperDeepgram)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})
	http.HandleFunc("/index.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFile(w, r, "index.js")
	})
	http.HandleFunc("/audio.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFile(w, r, "audio.js")
	})
	http.HandleFunc("/index.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		http.ServeFile(w, r, "index.css")
	})

	log.Info("serve", "port", port)

	for key, value := range deepgramQueryParams {
		log.Info("param", key, value)
	}

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

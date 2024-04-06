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
			_, message, err := browserConn.ReadMessage()
			if err != nil {
				log.Error("read from browser", "error", err)
				break
			}

			if err := serviceConn.WriteMessage(websocket.BinaryMessage, message); err != nil {
				log.Error("send to Deepgram", "error", err)
				break
			}
		}
	}()

	go func() {
		defer serviceConn.Close()
		defer browserConn.Close()
		for {
			_, message, err := serviceConn.ReadMessage()
			if err != nil {
				log.Error("read from Deepgram", "error", err)
				break
			}
			if err := browserConn.WriteMessage(websocket.TextMessage, message); err != nil {
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
	// curl -s https://api.openai.com/v1/audio/transcriptions \
	//    -H "Authorization: Bearer $OPENAI_API_KEY" \
	//    -H "Content-Type: multipart/form-data" \
	//    -F file="@$file" \
	//    -F model=whisper-1 \
	//    -F response_format=json

	// that's the curl command we want to replicate
	// we need to read the file from the request
	// and send it to the openai api
	// and then send the response back to the client

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
	_ = writer.WriteField("response_format", "json")

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
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
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

func main() {
	port := getEnv("PORT")

	http.HandleFunc("/transcribe", handler)
	http.HandleFunc("/whisper", whisper)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	log.Info("serve", "port", port)

	for key, value := range deepgramQueryParams {
		log.Info("param", key, value)
	}

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

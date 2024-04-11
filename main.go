package main

import (
	"bytes"
	"database/sql"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
)

var upgrader = websocket.Upgrader{} // use default options
var deepgramApiKey = getEnv("DEEPGRAM_API_KEY")
var openaiApiKey = getEnv("OPENAI_API_KEY")

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

func handleTranscribe(w http.ResponseWriter, r *http.Request, db *sql.DB) {
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

func whisper(w http.ResponseWriter, r *http.Request, db *sql.DB) {
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
	if err == nil {
		err = saveEventToDB(db, "whisper", 0, string(body.Bytes()))
	}
	if err != nil {
		handleError(w, err)
		return
	}
	defer resp.Body.Close()

	handleResponse(w, resp)
}

func whisperDeepgram(w http.ResponseWriter, r *http.Request, db *sql.DB) {
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

	resp, err := sendDeepgramRequest(audioData)
	if err == nil {
		err = saveEventToDB(db, "whisper-deepgram", 0, string(audioData))
	}
	if err != nil {
		handleError(w, err)
		return
	}
	defer resp.Body.Close()

	handleResponse(w, resp)
}

func handleOpenAIProxy(w http.ResponseWriter, r *http.Request, db *sql.DB) {
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
	if err == nil {
		body, _ := io.ReadAll(r.Body)
		err = saveEventToDB(db, path, 0, string(body))
	}
	if err != nil {
		log.Error("Error sending request", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w, resp)
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Error("Error copying response body", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	db, err := initDB()
	if err != nil {
		log.Fatal("Error initializing database", "error", err)
	}
	defer db.Close()
	port := getEnv("PORT")

	http.Handle("/transcribe", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleTranscribe(w, r, db)
	}))
	http.Handle("/whisper", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		whisper(w, r, db)
	}))
	http.Handle("/whisper-deepgram", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		whisperDeepgram(w, r, db)
	}))
	http.Handle("/openai/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleOpenAIProxy(w, r, db)
	}))

	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	log.Info("serve", "port", port)

	for key, value := range deepgramQueryParams {
		log.Info("param", key, value)
	}

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

func sendDeepgramRequest(audioData []byte) (*http.Response, error) {
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
	q.Set("language", "en-US")
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
func initDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "events.db")
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS events (
		key TEXT,
		seq INTEGER,
		time INTEGER,
		payload TEXT
	)`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func saveEventToDB(db *sql.DB, key string, seq int, payload string) error {
	_, err := db.Exec(`INSERT INTO events (key, seq, time, payload) VALUES (?, ?, ?, ?)`, key, seq, time.Now().Unix(), payload)
	return err
}

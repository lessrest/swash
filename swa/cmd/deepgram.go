package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

var (
	deepgramAPIKey = os.Getenv("DEEPGRAM_API_KEY")
)

func deepgramURL(language string) string {
	queryParams := url.Values{
		"model":           []string{"nova-2"},
		"interim_results": []string{"true"},
		"smart_format":    []string{"true"},
		"vad_events":      []string{"false"},
		"diarize":         []string{"true"},
		"language":        []string{language},
		"encoding":        []string{"opus"},
		"sample_rate":     []string{"48000"},
	}
	return fmt.Sprintf("wss://api.deepgram.com/v1/listen?%s", queryParams.Encode())
}

func handleTranscribe(w http.ResponseWriter, r *http.Request) {
	language := r.URL.Query().Get("language")
	if language == "" {
		language = "en-US"
	}

	log.Println("consumes stream of audio blobs")
	log.Println("produces stream of transcription events")

	deepgramConn, _, err := websocket.DefaultDialer.Dial(deepgramURL(language), http.Header{
		"Authorization": []string{fmt.Sprintf("Token %s", deepgramAPIKey)},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer deepgramConn.Close()

	log.Printf("became a web socket at %s", time.Now())

	upgrader := websocket.Upgrader{}
	browserConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer browserConn.Close()

	go forwardMessages(browserConn, deepgramConn)
	go forwardMessages(deepgramConn, browserConn)

	select {}
}

func forwardMessages(from, to *websocket.Conn) {
	for {
		messageType, data, err := from.ReadMessage()
		if err != nil {
			log.Printf("error reading message: %v", err)
			return
		}

		err = to.WriteMessage(messageType, data)
		if err != nil {
			log.Printf("error writing message: %v", err)
			return
		}
	}
}

func handleWhisperDeepgram(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	language := r.URL.Query().Get("language")
	if language == "" {
		language = "en-US"
	}

	audioData, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := http.Post(
		fmt.Sprintf("https://api.deepgram.com/v1/listen?model=nova-2&language=%s&diarize=true&smart_format=true", language),
		"audio/webm",
		bytes.NewReader(audioData),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(w, resp.Body)
		return
	}

	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResp, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonResp)
}

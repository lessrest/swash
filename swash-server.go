package main

import (
	"net/http"
	"net/url"
	"os"

	"github.com/charmbracelet/log"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{} // use default options
var deepgramApiKey = getEnv("DEEPGRAM_API_KEY")

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

func main() {
	port := getEnv("PORT")

	http.HandleFunc("/transcribe", handler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	log.Info("serve", "port", port)

	for key, value := range deepgramQueryParams {
		log.Info("param", key, value)
	}

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

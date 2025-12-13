package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mbrock/swash/internal/tty"
)

type basicSession interface {
	Gist() (HostStatus, error)
	SendInput(data string) (int, error)
	Kill() error
	Restart() error
	SessionID() (string, error)
}

type UnixServer struct {
	socketPath string
	ln         net.Listener
	srv        *http.Server
}

func ServeUnix(socketPath string, sess basicSession, ttyHost *tty.TTYHost) (*UnixServer, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("unix socket path is empty")
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating socket dir: %w", err)
	}
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /gist", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		st, err := sess.Gist()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, st)
	})

	mux.HandleFunc("POST /input", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		n, err := sess.SendInput(string(b))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"bytes": n})
	})

	mux.HandleFunc("POST /kill", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		if err := sess.Kill(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /restart", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		if err := sess.Restart(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	if ttyHost != nil {
		registerTTYHandlers(mux, ttyHost)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	us := &UnixServer{socketPath: socketPath, ln: ln, srv: srv}
	go srv.Serve(ln)
	return us, nil
}

func (s *UnixServer) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	if s.ln != nil {
		_ = s.ln.Close()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
	return nil
}

func registerTTYHandlers(mux *http.ServeMux, h *tty.TTYHost) {
	mux.HandleFunc("GET /tty/screen", func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		switch format {
		case "text":
			text, err := h.GetScreenText()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, text)
		default:
			ansi, err := h.GetScreenANSI()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, ansi)
		}
	})

	mux.HandleFunc("GET /tty/cursor", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		row, col, err := h.GetCursor()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"row": row, "col": col})
	})

	mux.HandleFunc("GET /tty/title", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		title, err := h.GetTitle()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, title)
	})

	mux.HandleFunc("GET /tty/mode", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		alt, err := h.GetMode()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"alt_screen": alt})
	})

	mux.HandleFunc("POST /tty/resize", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var in struct {
			Rows int32 `json:"rows"`
			Cols int32 `json:"cols"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := h.Resize(in.Rows, in.Cols); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /tty/clients", func(w http.ResponseWriter, r *http.Request) {
		_ = r
		count, rows, cols, err := h.GetAttachedClients()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"count": count, "rows": rows, "cols": cols})
	})

	mux.HandleFunc("POST /tty/detach", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var in struct {
			ClientID string `json:"client_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if in.ClientID == "" {
			http.Error(w, "client_id required", http.StatusBadRequest)
			return
		}
		if err := h.Detach(in.ClientID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("GET /tty/attach", func(w http.ResponseWriter, r *http.Request) {
		rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
		cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
		outR, inW, remoteRows, remoteCols, screenANSI, clientID, err := tty.AttachIO(h, int32(rows), int32(cols))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		hj, ok := w.(http.Hijacker)
		if !ok {
			outR.Close()
			inW.Close()
			http.Error(w, "hijacking not supported", http.StatusInternalServerError)
			return
		}

		conn, rw, err := hj.Hijack()
		if err != nil {
			outR.Close()
			inW.Close()
			return
		}

		bw := rw.Writer
		if bw == nil {
			bw = bufio.NewWriter(conn)
		}

		// Send handshake HTTP response with the initial snapshot as the body.
		screenBytes := []byte(screenANSI)
		fmt.Fprintf(bw, "HTTP/1.1 200 OK\r\n")
		fmt.Fprintf(bw, "Content-Length: %d\r\n", len(screenBytes))
		fmt.Fprintf(bw, "Content-Type: text/plain\r\n")
		fmt.Fprintf(bw, "X-Swash-Rows: %d\r\n", remoteRows)
		fmt.Fprintf(bw, "X-Swash-Cols: %d\r\n", remoteCols)
		fmt.Fprintf(bw, "X-Swash-Client-Id: %s\r\n", clientID)
		fmt.Fprintf(bw, "\r\n")
		bw.Write(screenBytes)
		bw.Flush()

		// After the snapshot, treat conn as a raw stream.
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(conn, outR)
			close(done)
		}()

		_, _ = io.Copy(inW, conn)
		_ = inW.Close()
		_ = outR.Close()
		_ = conn.Close()
		<-done
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

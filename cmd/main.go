package main

import (
	"encoding/json"
	"flag"
	"fmt"
	gondola2 "github.com/stillmatic/gazelle-inference-demo/pkg/gondola"
	"github.com/stillmatic/gazelle-inference-demo/pkg/logutil"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wsw"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

var addr = flag.String("addr", "localhost:8081", "http service address")
var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))

var upgrader = websocket.Upgrader{
	// TODO: add origin check
	CheckOrigin: func(r *http.Request) bool { return true },
}

func echo(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()
	for {
		mt, message, err := c.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			break
		}
		log.Printf("recv: %s", message)
		err = c.WriteMessage(mt, message)
		if err != nil {
			log.Println("write:", err)
			break
		}
	}
}

type Server struct {
}

func (s *Server) handleInitialWebsocketMessage(data []byte, ws *wsw.WSWrapper) (*gondola2.Orchestrator, error) {
	var tmp gondola2.WebSocketMessage
	if err := json.Unmarshal(data, &tmp); err != nil {
		return nil, err
	}
	if tmp.Type != gondola2.WebSocketAudioConfigStart {
		return nil, fmt.Errorf("first message must be WebSocketAudioConfigStart, received %s", tmp.Type)
	}
	var msg gondola2.AudioConfigStartMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	if msg.ConversationID == nil {
		return nil, fmt.Errorf("conversation ID is required")
	}
	return gondola2.NewOrchestrator(msg, ws)
}

func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	logger.Info("received request", "method", r.Method, "url", r.URL)
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		c.WriteMessage(websocket.CloseMessage, []byte(`{"status": "error"}`))
		c.Close()
		logger.Error("error upgrading websocket connection", "error", err)
		return
	}
	defer c.Close()
	logger.Info("websocket connection established")

	// special case first message
	_, msgData, err := c.ReadMessage()
	if err != nil {
		log.Println("read:", err)
		return
	}
	//logger.Info("received message", "message", string(msgData))
	wsWrapper := wsw.NewWSWrapper(c)

	ctx := r.Context()
	ctx = logutil.ContextWithLogger(ctx, logger)
	conv, err := s.handleInitialWebsocketMessage(msgData, wsWrapper)
	if err != nil {
		logger.ErrorContext(ctx, "error handling websocket message", "error", err)
		_ = wsWrapper.WriteMessage(websocket.CloseMessage, []byte(`{"status": "error"}`))
		return
	}
	err = wsWrapper.WriteJSON(&gondola2.ReadyMessage{
		WebSocketMessage: gondola2.WebSocketMessage{
			Type: gondola2.WebSocketReady,
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "error writing ready message", "error", err)
		return
	}
	err = conv.Start(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "error starting orchestrator", "error", err)
	}
	//lint:ignore S1000 reason: explicit for-select to wait for context to be done
	for {
		select {
		case <-ctx.Done():
			return
		}
	}
	logger.Warn("exiting conversation handler")
}

func home(w http.ResponseWriter, r *http.Request) {
	// return a 200 JSON
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "ok"}`))
}

func main() {
	flag.Parse()
	log.SetFlags(0)
	godotenv.Load()

	shouldLogDebug := os.Getenv("DEBUG")
	if shouldLogDebug == "1" {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	ms := Server{}

	http.HandleFunc("/echo", echo)
	http.HandleFunc("/conversation", ms.conversation)
	http.HandleFunc("/", home)
	logger.Info("listening", "addr", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

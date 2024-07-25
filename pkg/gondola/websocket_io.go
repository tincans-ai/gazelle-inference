package gondola

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wsw"
	"time"
)

type WebsocketInputStreamer interface {
	Stream(ctx context.Context) chan types.GondolaMessage
}
type HandleMessage func(data []byte, outChan chan types.GondolaMessage) (error, bool)

type WebsocketInput struct {
	ws         *wsw.WSWrapper
	handleFunc HandleMessage
}

func (w *WebsocketInput) Stream(ctx context.Context) chan types.GondolaMessage {
	return w.stream(ctx, w.ws, w.handleFunc)
}

func (w *WebsocketInput) stream(ctx context.Context, ws *wsw.WSWrapper, handleMessage HandleMessage) chan types.GondolaMessage {
	ctx, cancel := context.WithCancel(ctx)
	outChan := make(chan types.GondolaMessage)
	sendErr := func(err error) {
		outChan <- types.GondolaMessage{
			Err:       err,
			Timestamp: time.Now(),
		}
	}

	// cleanup goroutine when websocket is closed
	go func() {
		for {
			select {
			case <-ctx.Done():
				err := ws.Close()
				if err != nil {
					err = fmt.Errorf("error closing websocket %w", err)
					sendErr(err)
					return
				}
			}
		}
	}()

	// read message, decode base64, send to out
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				// if this is closing, finish the context
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					_, cancel := context.WithCancel(ctx)
					cancel()
					return
				} else {
					err = fmt.Errorf("error reading websocket message %w", err)
					sendErr(err)
					return
				}
			}

			err, isCancel := handleMessage(data, outChan)
			if err != nil {
				sendErr(err)
				return
			} else if isCancel {
				cancel()
				return
			}

		}
	}()
	return outChan
}
func NewWebsocketInput(ws *wsw.WSWrapper) *WebsocketInput {
	logger.Debug("returning new websocket input")
	return &WebsocketInput{
		ws:         ws,
		handleFunc: handleWebsocketMessage,
	}
}

func handleWebsocketMessage(data []byte, outChan chan types.GondolaMessage) (error, bool) {
	tmp := WebSocketMessage{}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return fmt.Errorf("error unmarshaling websocket message %w", err), false
	}
	switch tmp.Type {
	case WebSocketAudio:
		var msg AudioMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			err = fmt.Errorf("error unmarshaling WebSocketAudio message %w", err)
			return err, false
		}
		// convert msg.Data from base64 to bytes
		// this is a little bit slow in Golang but it's DEFINITELY not the bottleneck
		decoded, err := base64.StdEncoding.DecodeString(msg.Data)
		if err != nil {
			err = fmt.Errorf("error decoding base64 audio data %w", err)
			return err, false
		}
		logger.Debug("got audio message", "len", len(decoded))
		outChan <- types.GondolaMessage{
			Audio:       decoded,
			Timestamp:   time.Now(),
			MessageType: types.MessageTypeRecordedAudio,
		}
		return nil, false
	case WebSocketStop:
		return nil, true
	default:
		return fmt.Errorf("unknown/unsupported message type %s", tmp.Type), false
	}
}

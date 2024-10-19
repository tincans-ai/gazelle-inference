package gondola

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/gorilla/websocket"
	"github.com/rs/xid"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"net/http"
	"os"
	"time"
)

type OpenAIClient struct {
	dialer *websocket.Conn
	currID xid.ID
}

func NewOpenAIClient(apiKey string) *OpenAIClient {
	uri := "wss://api.openai.com/v1/realtime?model=gpt-4o-realtime-preview-2024-10-01"

	header := http.Header{}
	header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	header.Set("OpenAI-Beta", "realtime=v1")

	ws, _, err := websocket.DefaultDialer.Dial(uri, header)
	if err != nil {
		panic(err)
	}

	return &OpenAIClient{dialer: ws}
}

type oaiAudioSendMessage struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
}

type oaiAudioResponse struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

func (c *OpenAIClient) SetCurrID(id xid.ID) {
	c.currID = id
}

func (c *OpenAIClient) Process(ctx context.Context, input <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	output := make(chan types.GondolaMessage)
	bb := &bytes.Buffer{}
	b64Encoder := base64.NewEncoder(base64.StdEncoding, bb)

	go func() {
		defer b64Encoder.Close()
		defer c.dialer.Close()
		defer close(output)

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-input:
				if msg.MessageType != types.MessageTypeRecordedAudio {
					logger.ErrorContext(ctx, "Received unexpected message type", "message", msg)
				}
				if msg.EOF {
					err := c.dialer.WriteMessage(websocket.TextMessage, []byte("EOF"))
					if err != nil {
						logger.ErrorContext(ctx, "Error writing EOF to websocket", "error", err)
					}
					continue
				}

				_, err := b64Encoder.Write(msg.Audio)
				if err != nil {
					logger.ErrorContext(ctx, "Error writing audio to base64", "error", err)
				}

				sendMsg := oaiAudioSendMessage{
					Type:  "input_audio_buffer.append",
					Audio: bb.String(),
				}

				marshaled, err := json.Marshal(sendMsg)
				if err != nil {
					logger.ErrorContext(ctx, "Error marshaling message to JSON", "error", err)
				}
				err = c.dialer.WriteMessage(
					websocket.TextMessage,
					marshaled,
				)
				if err != nil {
					logger.ErrorContext(ctx, "Error writing message to websocket", "error", err)
				}
				bb.Reset()
			}
		}
	}()

	go func() {
		for {
			_, message, err := c.dialer.ReadMessage()
			if err != nil {
				logger.ErrorContext(ctx, "Error reading message from websocket", "error", err)
				return
			}
			var response oaiAudioResponse
			err = json.Unmarshal(message, &response)
			if err != nil {
				logger.ErrorContext(ctx, "Error unmarshaling message from JSON", "error", err)
			}

			switch response.Type {
			case "session.created":
				logger.Info("OAI Session created")
			case "response.audio.delta":
				//	pop audio and decode from b64 and send to output
				audioStr := response.Delta
				audio, err := base64.StdEncoding.DecodeString(audioStr)
				if err != nil {
					logger.ErrorContext(ctx, "Error decoding audio from base64", "error", err)
				}
				logger.InfoContext(ctx, "Received audio from OpenAI", "audio", len(audio))
				output <- types.GondolaMessage{
					Audio:       audio,
					MessageType: types.MessageTypeSynthesizerOutput,
					Err:         nil,
					Timestamp:   time.Now(),
					GazelleID:   c.currID,
				}
			case "response.audio.done":
				output <- types.GondolaMessage{
					MessageType: types.MessageTypeSynthesizerOutput,
					EOF:         true,
					Timestamp:   time.Now(),
					GazelleID:   c.currID,
				}

			default:
				logger.WarnContext(ctx, "Received unexpected message type", "message", response)
			}
		}
	}()

	return output
}

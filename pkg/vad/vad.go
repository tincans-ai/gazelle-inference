package vad

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/stillmatic/gazelle-inference-demo/pkg/logutil"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wavencoder"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wsw"

	"net/http"
	"time"

	"log/slog"

	"github.com/gorilla/websocket"
)

type SileroVAD struct {
	conn         *wsw.WSWrapper
	samplingRate int
}

type SileroMessageType string

type SileroMessage struct {
	MessageType SileroMessageType `json:"message_type"`
	Message     string            `json:"message"`
}

type SileroInitialData struct {
	Threshold            float64 `json:"threshold"`
	SamplingRate         int     `json:"sampling_rate"`
	MinSilenceDurationMS *int    `json:"min_silence_duration_ms,omitempty"`
	SpeechPadMS          *int    `json:"speech_pad_ms,omitempty"`
}

type SileroInitialMessage struct {
	MessageType   SileroMessageType `json:"message_type"`
	Data          SileroInitialData `json:"data"`
	Authorization string            `json:"authorization"`
}

type SileroVADEvent struct {
	Start *int `json:"start,omitempty"`
	End   *int `json:"end,omitempty"`
}

type SileroVADEventMessage struct {
	MessageType SileroMessageType `json:"message_type"`
	Event       SileroVADEvent    `json:"event"`
}

type SileroAudioMessage struct {
	MessageType SileroMessageType `json:"message_type"`
	// Data is b64 encoded audio
	Data string `json:"data"`
}

const (
	SileroMessageTypeInitial  SileroMessageType = "vad_initial_message"
	SileroMessageTypeAck      SileroMessageType = "ack"
	SileroMessageTypeVADEvent SileroMessageType = "vad_event"
	SileroMessageTypeAudio    SileroMessageType = "websocket_audio"
)

func NewSileroVAD(ctx context.Context, url string, samplingRate int) (*SileroVAD, error) {
	client := websocket.DefaultDialer
	headers := http.Header{}
	conn, resp, err := client.DialContext(ctx, url, headers)
	if err != nil {
		return nil, fmt.Errorf("error dialing to VAD websocket at url %s: %w", url, err)
	}
	// TODO: is this necessary?
	_ = resp
	wrapper := wsw.NewWSWrapper(conn)

	msdms := 400
	spm := 50
	// initialize connection with a handshake
	err = wrapper.WriteJSONConcurrent(SileroInitialMessage{
		MessageType: SileroMessageTypeInitial,
		Data: SileroInitialData{
			Threshold:            0.5,
			SamplingRate:         samplingRate,
			MinSilenceDurationMS: &msdms,
			SpeechPadMS:          &spm,
		},
		// TODO: have a real auth handshake
		Authorization: "helloworld",
	})
	if err != nil {
		return nil, fmt.Errorf("error sending initial message to vad %w", err)
	}
	var ackMsg SileroMessage
	err = wrapper.ReadJSON(&ackMsg)
	if err != nil {
		return nil, fmt.Errorf("couldn't read ack msg: %w", err)
	}
	if ackMsg.MessageType != SileroMessageTypeAck {
		return nil, fmt.Errorf("first vad message must be ack message")
	}

	return &SileroVAD{
		conn:         wrapper,
		samplingRate: samplingRate,
	}, nil
}

func (s *SileroVAD) Process(ctx context.Context, inpChan <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	outChan := make(chan types.GondolaMessage)
	logger := logutil.LoggerFromContext(ctx)
	// process responses from the VAD
	go func() {
		for {
			select {
			case <-ctx.Done():
				// cleanup
				return
			default:
				var msg SileroVADEventMessage
				err := s.conn.ReadJSON(&msg)
				if err != nil {
					outChan <- types.GondolaMessage{
						Err: fmt.Errorf("error reading from VAD websocket: %w", err),

						Timestamp: time.Now(),
					}
					return
				}
				if msg.MessageType != SileroMessageTypeVADEvent {
					outChan <- types.GondolaMessage{
						Err: fmt.Errorf("first message from VAD must be VAD event, received %s", msg.MessageType),
					}
					return
				}

				// handle start case
				if msg.Event.Start != nil {
					logger.Info("user started speaking", "start", *msg.Event.Start)
					outChan <- types.GondolaMessage{
						MessageType: types.MessageTypeStopResponse,
						Timestamp:   time.Now(),
					}
				}

				if msg.Event.End != nil {
					logger.Info("user done speaking", "end", *msg.Event.End)
					outChan <- types.GondolaMessage{
						MessageType: types.MessageTypeSendResponse,
						Timestamp:   time.Now(),
					}
				}
			}
		}
	}()

	// process sending audio to VAD
	go func() {
		// NOTE: silero only accepts 16000 hertz. We have modified the VAD in Python
		// to accept arbitrary sampling rates (set in the handshake) and it will resample
		// to the correct one. Therefore, we will send in the arbitrary sample rate and
		// let the VAD handle the resampling.
		wavEncoder := wavencoder.NewWavEncoder(s.samplingRate, 16, 1, 1)
		for {
			select {
			case <-ctx.Done():
				// cleanup
				return
			case msg := <-inpChan:
				// forward to VAD
				audio := msg.Audio
				if msg.MessageType == types.MessageTypeRecordedAudio {
					if len(audio) == 0 {
						slog.Warn("got empty audio message to VAD")
						continue
					}

					// encode audio to base64 wav
					encodedAudio, err := wavEncoder.ConvertAudio(ctx, audio)

					// TODO(Cinjon): Twilio is sending back 20ms chunks and the
					// VAD wants bigger chunks of 30ms. Buffer before sending.

					if err != nil {
						outChan <- types.GondolaMessage{
							Err:       fmt.Errorf("error encoding audio to wav: %w", err),
							Timestamp: time.Now(),
						}
						continue
					}
					b64Audio := base64.StdEncoding.EncodeToString(encodedAudio)
					sileroMsg := SileroAudioMessage{
						MessageType: SileroMessageTypeAudio,
						Data:        b64Audio,
					}
					s.conn.WriteJSONConcurrent(sileroMsg)
				} else {
					outChan <- types.GondolaMessage{
						Err:       fmt.Errorf("unknown message type to VAD: %v", msg.MessageType),
						Timestamp: time.Now(),
					}
				}
			}
		}
	}()
	return outChan
}

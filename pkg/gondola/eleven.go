// This synthesizer was not tested in this codebase.
package gondola

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
)

// VoiceSettings defines settings for a given generation.
// Implementation is copied from https://github.com/elevenlabs/elevenlabs-python/blob/7059dbaba46d06bdcda03b8cdb2dce06d21c07d2/elevenlabs/api/voice.py#L18
// The API docs are obviously wrong.
type VoiceSettings struct {
	Stability       float64  `json:"stability"`
	SimilarityBoost float64  `json:"similarity_boost"`
	Style           *float64 `json:"style,omitempty"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
}

type Voice struct {
	VoiceID     string            `json:"voice_id"`
	Name        *string           `json:"name,omitempty"`
	Category    *string           `json:"category,omitempty"`
	Description *string           `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Samples     []VoiceSample     `json:"samples,omitempty"`
	PreviewURL  *string           `json:"preview_url,omitempty"`
	Settings    *VoiceSettings    `json:"settings,omitempty"`
}

type VoiceSample struct {
	SampleID  string `json:"sample_id"`
	FileName  string `json:"file_name"`
	MimeType  string `json:"mime_type"`
	SizeBytes *int   `json:"size_bytes,omitempty"`
	Hash      string `json:"hash"`
}

type NormalizedAlignment struct {
	CharStartTimesMs []int    `json:"char_start_times_ms"`
	CharsDurationsMs []int    `json:"chars_durations_ms"`
	Chars            []string `json:"chars"`
}

type GenerationConfig struct {
	ChunkLengthSchedule []int `json:"chunk_length_schedule"`
}

type ElevenRequest struct {
	// Text should always end with a single space string " ".
	// In the first message, the text should be a space " ".
	Text                 string `json:"text"`
	TryTriggerGeneration bool   `json:"try_trigger_generation"`

	VoiceSettings    VoiceSettings    `json:"voice_settings,omitempty"`
	GenerationConfig GenerationConfig `json:"generation_config,omitempty"`

	XiAPIKey string `json:"xi_api_key"`
	// Confusing --  Should be provided only in the first message if not present in the header and the XI API Key is not provided.
	// unclear what the diff between the bearer token and api key is here
	// Authorization string `json:"authorization"`
}

type ElevenResponse struct {
	Audio               string              `json:"audio"`
	IsFinal             bool                `json:"isFinal"`
	NormalizedAlignment NormalizedAlignment `json:"normalizedAlignment"`
}

// BOS is a special request that signals the beginning of the stream.
var BOS = ElevenRequest{
	Text:                 " ",
	TryTriggerGeneration: true,
	// The one you’ll see most often is setting stability around 50 and similarity near 80, with minimal changes thereafter.
	// https://docs.elevenlabs.io/speech-synthesis/voice-settings
	VoiceSettings: VoiceSettings{
		Stability:       0.5,
		SimilarityBoost: 0.8,
	},
}

// EOS is a special request that signals the end of the stream.
var EOS = ElevenRequest{
	// Should always be an empty string "".
	Text: "",
}

type ElevenSynthesizer struct {
	// conn   *websocket.Conn
	apiKey  string
	url     string
	voiceID string
}

type ElevenPostRequestData struct {
	Text         string        `json:"text"`
	ModelID      string        `json:"model_id"`
	VoiceSetting VoiceSettings `json:"voice_settings"`
}

func NewElevenSynthesizer(apiKey string, voiceID string, modelID string) (*ElevenSynthesizer, error) {
	u := &url.URL{
		Scheme: "https",
		Host:   "api.elevenlabs.io",
		Path:   fmt.Sprintf("/v1/text-to-speech/%s/stream", voiceID),
	}

	// Add query parameters
	q := u.Query()
	// q.Set("model_id", modelID)
	q.Set("optimize_streaming_latency", "1")
	q.Set("output_format", "pcm_24000")
	u.RawQuery = q.Encode()

	url := u.String()

	slog.Info("Eleven synthesizer URL", "url", url, "apiKey", apiKey, "voiceID", voiceID)

	return &ElevenSynthesizer{
		// conn:   conn,
		url:     url,
		apiKey:  apiKey,
		voiceID: voiceID,
	}, nil
}

func (s *ElevenSynthesizer) Process(ctx context.Context, inp <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	outChan := make(chan types.GondolaMessage)
	go func() {
		for {
			select {
			case <-ctx.Done():
				slog.Warn("Eleven synthesizer shutting down")
				return
			case msg, ok := <-inp:
				if !ok {
					slog.Warn("Eleven synthesizer input channel closed")
					return
				}
				s.handleMessage(ctx, msg, outChan)
			}
		}
	}()
	return outChan
}

// handleMessage receives an v6.MessageTypeSynthesizerInput message, synthesizes it, and sends it to the output channel.
func (s *ElevenSynthesizer) handleMessage(ctx context.Context, msg types.GondolaMessage, outChan chan<- types.GondolaMessage) {
	sendErr := func(err error) {
		outChan <- types.GondolaMessage{
			MessageType: types.MessageTypeSynthesizerOutput,
			Err:         err,
			Timestamp:   time.Now(),

			Content: msg.Content,

			GazelleID:     msg.GazelleID,
			SynthesizerID: msg.SynthesizerID,
		}
	}

	client := http.Client{}
	headers := map[string]string{
		"Accept":       "audio/mpeg",
		"Content-Type": "application/json",
		"xi-api-key":   s.apiKey,
	}

	data := ElevenPostRequestData{
		Text:    msg.Content,
		ModelID: "eleven_monolingual_v1",
		VoiceSetting: VoiceSettings{
			Stability:       0.8,
			SimilarityBoost: 0.5,
		},
	}

	payload, err := json.Marshal(data)
	if err != nil {
		sendErr(fmt.Errorf("error marshaling data: %w", err))
		return
	}

	req := http.Request{
		Method: "POST",
		URL: &url.URL{
			Scheme: "https",
			Host:   "api.elevenlabs.io",
			Path:   fmt.Sprintf("/v1/text-to-speech/%s/stream", s.voiceID),
		},
		Header: http.Header{},
		Body:   io.NopCloser(bytes.NewReader(payload)),
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(&req)
	if err != nil {
		sendErr(fmt.Errorf("error making request: %w", err))
		return
	}
	defer resp.Body.Close()
	chunkSize := int(float64(GetChunkSizePerSecond(AudioEncodingLinear16, 24000)))
	var allAudio []byte
	audioChunk := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(audioChunk)
		if err == io.EOF {
			outChan <- types.GondolaMessage{
				Audio:       allAudio,
				Content:     msg.Content,
				Timestamp:   time.Now(),
				MessageType: types.MessageTypeSynthesizerOutput,

				GazelleID:     msg.GazelleID,
				SynthesizerID: msg.SynthesizerID,
			}
			break
		}
		if err != nil {
			sendErr(fmt.Errorf("error reading response body: %w", err))
			return
		}
		allAudio = append(allAudio, audioChunk[:n]...)
		if len(allAudio) > chunkSize {
			outChan <- types.GondolaMessage{
				Audio:         allAudio,
				Content:       msg.Content,
				Timestamp:     time.Now(),
				MessageType:   types.MessageTypeSynthesizerOutput,
				GazelleID:     msg.GazelleID,
				SynthesizerID: msg.SynthesizerID,
			}
			allAudio = make([]byte, 0)
			slog.Info("Sent audio to output channel", "content", msg.Content, "length", n)
		}
	}

}

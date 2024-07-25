package gondola

import (
	"context"
	"fmt"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const (
	XttsBaseUrl = "http://127.0.0.1:8000"
)

// XTTSClient is a client for communicating with a hosted XTTS API.
type XTTSClient struct {
	baseURL string
}

func NewXTTSClient(baseURL *string) (*XTTSClient, error) {
	var baseURLStr string
	if baseURL == nil {
		baseURLStr = XttsBaseUrl
	} else {
		baseURLStr = *baseURL
	}

	return &XTTSClient{
		baseURL: baseURLStr,
	}, nil
}

type StreamingInputs struct {
	SpeakerEmbedding []float64   `json:"speaker_embedding"`
	GptCondLatent    [][]float64 `json:"gpt_cond_latent"`
	Text             string      `json:"text"`
	Language         Language    `json:"language"`
	AddWavHeader     bool        `json:"add_wav_header"`
	StreamChunkSize  string      `json:"stream_chunk_size"`
	Decoder          string      `json:"decoder"`
}

type TTSToStreamRequest struct {
	Text       string   `json:"text"`
	SpeakerWav string   `json:"speaker_wav"`
	Language   Language `json:"language"`
}

type Language string

const (
	LanguageEN   Language = "en"
	LanguageDE   Language = "de"
	LanguageFR   Language = "fr"
	LanguageES   Language = "es"
	LanguageIT   Language = "it"
	LanguagePL   Language = "pl"
	LanguagePT   Language = "pt"
	LanguageTR   Language = "tr"
	LanguageRU   Language = "ru"
	LanguageNL   Language = "nl"
	LanguageCS   Language = "cs"
	LanguageAR   Language = "ar"
	LanguageZHCN Language = "zh-cn"
	LanguageJA   Language = "ja"
)

func (s *XTTSClient) Stream(ctx context.Context, text string) (io.ReadCloser, error) {
	url := s.baseURL + "/tts_stream"
	inps := TTSToStreamRequest{
		Text:       text,
		Language:   LanguageEN,
		SpeakerWav: "female.wav",
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("text", inps.Text)
	q.Add("language", string(inps.Language))
	q.Add("speaker_wav", inps.SpeakerWav)
	req.URL.RawQuery = q.Encode()

	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	return resp.Body, nil
}

type XTTSSynthesizer struct {
	client *XTTSClient
}

func NewXTTSSynthesizer(baseURL *string) (*XTTSSynthesizer, error) {
	client, err := NewXTTSClient(baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create XTTS client: %w", err)
	}
	return &XTTSSynthesizer{
		client: client,
	}, nil
}

func (s *XTTSSynthesizer) Process(ctx context.Context, inp <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	out := make(chan types.GondolaMessage)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				slog.Warn("XTTS synthesizer shutting down")
				return
			case msg, ok := <-inp:
				if !ok {
					return
				}

				s.handleMessage(ctx, msg, out)
			}
		}
	}()

	return out
}

// handleMessage receives an v6.MessageTypeSynthesizerInput message, synthesizes it, and sends it to the output channel.
func (s *XTTSSynthesizer) handleMessage(ctx context.Context, msg types.GondolaMessage, outChan chan<- types.GondolaMessage) {
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

	if msg.Content == "" {
		return
	}

	stream, err := s.client.Stream(ctx, msg.Content)
	if err != nil {
		err = fmt.Errorf("error calling Coqui API: %w", err)
		sendErr(err)
	}
	defer stream.Close()
	chunkSize := int(float64(GetChunkSizePerSecond(AudioEncodingLinear16, 24000)))
	var allAudio []byte
	audioChunk := make([]byte, 4096)
	for {
		n, err := stream.Read(audioChunk)
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
				Audio:       allAudio,
				Content:     msg.Content,
				Timestamp:   time.Now(),
				MessageType: types.MessageTypeSynthesizerOutput,

				GazelleID:     msg.GazelleID,
				SynthesizerID: msg.SynthesizerID,
			}
			allAudio = make([]byte, 0)
			//slog.Info("Sent audio to output channel", "content", msg.Content, "length", n)
		}
	}
}

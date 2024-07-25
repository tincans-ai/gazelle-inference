package gondola

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"time"

	"github.com/stillmatic/gazelle-inference-demo/pkg/playht"
	v1 "github.com/stillmatic/gazelle-inference-demo/pkg/playht/v1"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// PlayhtAddr is a very strange constant.
// There are multiple references and not sure which is the right one, it's also overriden when you actually get the
// lease back...
// https://github.com/playht/pyht/blob/85ff11f3f577db523979778512f5788444d6e4be/pyht/lease.py#L9-L10C21
const PlayhtAddr = "prod.turbo.play.ht:443"

type PlayHTClient struct {
	client v1.TtsClient
	lease  *playht.Lease
}

type PlayHTConfig struct {
	ID           string
	Secret       string
	SamplingRate int
	OutputFormat string
}

func NewPlayHTClient(cfg PlayHTConfig) (*PlayHTClient, error) {
	factory := playht.NewLeaseFactory(
		cfg.ID,
		cfg.Secret,
		playht.DefaultAPIURL,
	)
	lease, err := factory.NewLease()
	if err != nil {
		err = fmt.Errorf("could not get play.ht lease: %w", err)
		return nil, err
	}
	connAddr := PlayhtAddr
	infAddress := lease.GetPremiumInferenceAddress()
	//infAddress := lease.GetInferenceAddress()
	if infAddress != nil {
		//slog.Info("using inference address from lease", "address", *infAddress)
		connAddr = *infAddress
	}

	if err != nil {
		return nil, fmt.Errorf("could not load tls cert: %w", err)
	}

	conn, err := grpc.Dial(connAddr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	if err != nil {
		return nil, fmt.Errorf("could not connect to play.ht: %w", err)
	}

	client := v1.NewTtsClient(conn)
	return &PlayHTClient{
		client: client,
		lease:  lease,
	}, nil
}

type PlayHTSynthesizer struct {
	client       *PlayHTClient
	samplingRate int32
	outputFormat string
}

func NewPlayHTSynthesizer(cfg PlayHTConfig) (*PlayHTSynthesizer, error) {
	client, err := NewPlayHTClient(cfg)
	if err != nil {
		return nil, err
	}
	return &PlayHTSynthesizer{
		client:       client,
		samplingRate: int32(cfg.SamplingRate),
		outputFormat: cfg.OutputFormat,
	}, nil
}

func float32ToInt16(f float32) int16 {
	if f >= 1.0 {
		return math.MaxInt16
	} else if f <= -1.0 {
		return math.MinInt16
	}
	return int16(f * float32(math.MaxInt16))
}

func float32ToInt8(f float32) int8 {
	if f >= 1.0 {
		return math.MaxInt8
	} else if f <= -1.0 {
		return math.MinInt8
	}
	return int8(f * float32(math.MaxInt8))
}

func fp32to16(inp []byte) (out []byte) {
	if len(inp) == 0 {
		return nil
	}
	if len(inp)%4 != 0 {
		slog.Error("Input byte array length must be a multiple of 4 (size of a float32)")
		return nil
	}

	numSamples := len(inp) / 4
	float32Samples := make([]float32, numSamples)
	buffer := bytes.NewBuffer(inp)

	// Read the bytes into the float32 slice
	if err := binary.Read(buffer, binary.LittleEndian, &float32Samples); err != nil {
		slog.Error("binary.Read failed:", "error", err)
		return nil
	}

	// Create a buffer to write the output 16-bit PCM samples
	outBuffer := new(bytes.Buffer)

	// Convert float32 samples to int16 and write to buffer
	for _, sample := range float32Samples {
		int16Sample := float32ToInt16(sample)
		if err := binary.Write(outBuffer, binary.LittleEndian, int16Sample); err != nil {
			slog.Error("binary.Write failed:", "error", err)
			return nil
		}
	}

	return outBuffer.Bytes()
}

func (s *PlayHTSynthesizer) Process(ctx context.Context, inp <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	outChan := make(chan types.GondolaMessage)
	go func() {
		for {
			select {
			case <-ctx.Done():
				slog.Warn("PlayHT synthesizer shutting down")
				return
			case msg, ok := <-inp:
				if !ok {
					slog.Warn("PlayHT synthesizer input channel closed")
					return
				}
				s.handleMessage(ctx, msg, outChan)
			}
		}
	}()
	return outChan
}

// handleMessage receives an v6.MessageTypeSynthesizerInput message, synthesizes it, and sends it to the output channel.
func (s *PlayHTSynthesizer) handleMessage(ctx context.Context, msg types.GondolaMessage, outChan chan<- types.GondolaMessage) {
	sendErr := func(err error) {
		outChan <- types.GondolaMessage{
			MessageType:   types.MessageTypeSynthesizerOutput,
			Err:           err,
			Timestamp:     time.Now(),
			GazelleID:     msg.GazelleID,
			SynthesizerID: msg.SynthesizerID,

			Content: msg.Content,
		}
	}
	// https://github.com/playht/pyht/blob/85ff11f3f577db523979778512f5788444d6e4be/pyht/async_client.py#L154C94-L154C94
	// Suggests using 'draft' for high quality, 'medium' for faster speed.
	quality := v1.Quality_QUALITY_DRAFT
	format := v1.Format_FORMAT_RAW
	isMulaw := s.outputFormat == "mulaw"
	sampleRate := int32(24000)
	switch s.outputFormat {
	case "raw":
		format = v1.Format_FORMAT_RAW
	case "mulaw":
		format = v1.Format_FORMAT_MULAW
		sampleRate = int32(8000)
	default:
		sendErr(fmt.Errorf("unknown output format %s", s.outputFormat))
		return
	}
	// the default is 24000, explicitly setting here

	if msg.Content == "" {
		return
	}

	req := &v1.TtsRequest{
		Lease: s.client.lease.Data(),
		Params: &v1.TtsParams{
			Text:       []string{msg.Content},
			Quality:    &quality,
			Format:     &format,
			SampleRate: &sampleRate,
			// SampleRate: &s.samplingRate,
			// This voice name is ludicrously stupid to get.
			Voice: "s3://voice-cloning-zero-shot/d9ff78ba-d016-47f6-b0ef-dd630f59414e/female-cs/manifest.json",
		},
	}
	//slog.Info("calling playht", "request", req)
	resp, err := s.client.client.Tts(ctx, req)
	if err != nil {
		err := fmt.Errorf("error synthesizing outcome: %w", err)
		sendErr(err)
		return
	}
	// stream GRPC response
	for {
		chunk, err := resp.Recv()
		if err == io.EOF {
			if chunk == nil {
				err := fmt.Errorf("received nil chunk")
				sendErr(err)
				break
			}
			decoded := fp32to16(chunk.Data)

			outChan <- types.GondolaMessage{
				MessageType: types.MessageTypeSynthesizerOutput,
				Err:         nil,
				Timestamp:   time.Now(),
				EOF:         true,
				Audio:       decoded,

				GazelleID:     msg.GazelleID,
				SynthesizerID: msg.SynthesizerID,
			}
			break
		}
		if err != nil {
			err := fmt.Errorf("error receiving chunk: %w", err)
			sendErr(err)
			return
		}
		// slog.Debug("received chunk", "chunk", chunk, "status", chunk.Status)
		var decoded []byte
		if isMulaw {
			decoded = chunk.Data
		} else {
			decoded = fp32to16(chunk.Data)
		}
		outChan <- types.GondolaMessage{
			MessageType: types.MessageTypeSynthesizerOutput,
			Err:         nil,
			Timestamp:   time.Now(),

			Audio: decoded,

			GazelleID:     msg.GazelleID,
			SynthesizerID: msg.SynthesizerID,
		}
	}
}

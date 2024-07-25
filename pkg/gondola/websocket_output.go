package gondola

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/rs/xid"
	"github.com/stillmatic/gazelle-inference-demo/pkg/hb"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wavencoder"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wsw"
)

type WebsocketOutputter interface {
	Sink(ctx context.Context, inp <-chan types.GondolaMessage) <-chan error
	Start(ctx context.Context, gazelleID xid.ID, synthesizerID xid.ID)
	// SetSynthesizerEOF indicates what the last synthesizerID will be for a given gazelleID
	SetSynthesizerEOF(gazelleID xid.ID, synthesizerID xid.ID)
}

type BaseWebsocketOutput struct {
	ws *wsw.WSWrapper
	// mu guards currentRequestID
	mu sync.Mutex

	hbuf     *hb.HierarchicalBuffer
	stopChan chan xid.ID

	mediaFormat     MediaFormat
	secondsPerChunk float64

	handleChunk  func(ctx context.Context, chunk []byte) (msgJson any, err error)
	startedComps map[xid.ID]bool
}

// Sink receives data from the orchestrator and buffers it to potentially write to the websocket.
func (w *BaseWebsocketOutput) Sink(ctx context.Context, inp <-chan types.GondolaMessage) <-chan error {
	errChan := make(chan error)
	go func(ctx context.Context) {
		for {
			select {
			case <-ctx.Done():
				err := w.ws.Close()
				if err != nil {
					errChan <- fmt.Errorf("error closing websocket %w", err)
					return
				}
			case msg := <-inp:
				// Clear buffer if new request ID
				//slog.DebugContext(ctx, "[websocketOutput] received message", "gazelleID", msg.GazelleID, "synthesizerID", msg.SynthesizerID, "content", msg.Content, "audio", len(msg.Audio))
				// Write to hierarchal buffer
				w.hbuf.Add(msg.GazelleID, msg.SynthesizerID, msg.Audio)
			}
		}
	}(ctx)
	return errChan
}

func (w *BaseWebsocketOutput) SetSynthesizerEOF(gazelleID xid.ID, synthesizerID xid.ID) {
	w.hbuf.SetEofMap(gazelleID, synthesizerID)
}

func GetChunkSizePerSecond(audioEncoding AudioEncoding, samplingRate int) int {
	if audioEncoding == AudioEncodingLinear16 {
		return samplingRate * 2
	} else if audioEncoding == AudioEncodingMulaw {
		return samplingRate
	} else {
		panic("Unsupported audio encoding")
	}
}
func (w *BaseWebsocketOutput) getChunkSize(secondsPerChunk float64) int {
	return int(secondsPerChunk * float64(GetChunkSizePerSecond(
		AudioEncoding(w.mediaFormat.Encoding), w.mediaFormat.SampleRate)))
}

func (w *BaseWebsocketOutput) tick(ctx context.Context, gazelleID xid.ID, synthesizerID xid.ID) {
	// process a chunk that ideally ends at a zero crossing
	chunkSize := w.getChunkSize(w.secondsPerChunk)
	chunk, done, _ := w.hbuf.NextZC(ctx, gazelleID, chunkSize)
	if len(chunk) > 0 {
		msgJson, err := w.handleChunk(ctx, chunk)
		if err != nil {
			logger.ErrorContext(ctx, "error handling chunk", "error", err)
			return
		}

		err = w.ws.WriteJSONConcurrent(msgJson)
		if err != nil {
			logger.ErrorContext(ctx, "error writing to websocket, hanging up", "error", err)
			// close the context to stop the orchestrator
			// this happens if user hangs up
			_, cancel := context.WithCancel(ctx)
			cancel()
		}
	}
	logger.DebugContext(ctx, "buffer not empty, outputting", "gazelleID", gazelleID, "synthesizerID", synthesizerID, "done", done)
	if w.hbuf.Len(gazelleID) == 0 && done {
		logger.InfoContext(ctx, "buffer empty, done outputting", "gazelleID", gazelleID, "synthesizerID", synthesizerID)
		w.stopChan <- gazelleID
	}
}

func (w *BaseWebsocketOutput) start(ctx context.Context, gazelleID xid.ID, synthReqID xid.ID) {
	//chunkSize := w.getChunkSize(w.secondsPerChunk)
	go func() {
		// multiplier should be under 1 to account for some jitter
		// TODO: feature flag this
		multiplier := 1.0 // 0.9
		duration := time.Duration(multiplier * w.secondsPerChunk * float64(time.Second))
		ticker := time.NewTicker(duration)
		defer ticker.Stop()

		//logger.InfoContext(ctx, "[websocketOutput] starting loop", "bufferSize", w.hbuf.Len(gazelleID), "gazelleID", gazelleID.String(), "synthesizerID", synthReqID.String())

		for {
			select {
			case <-ticker.C:
				w.tick(ctx, gazelleID, synthReqID)
			case <-w.stopChan:
				logger.InfoContext(ctx, "stopping output", "gazelleID", gazelleID.String(), "synthesizerID", synthReqID.String())
				return
			}
		}
	}()
}

// WebsocketOutput writes data to a websocket. It only pumps out if allowed.
type WebsocketOutput struct {
	BaseWebsocketOutput
	encoder     *wavencoder.WavEncoder
	timingCache *TimingCache
}

func NewWebsocketOutput(ctx context.Context, ws *wsw.WSWrapper, outputSamplingRate int, timingCache *TimingCache) *WebsocketOutput {
	hbuf := hb.NewHierarchicalBuffer()
	encoder := wavencoder.NewWavEncoder(outputSamplingRate, 16, 1, 1)

	w := &WebsocketOutput{
		BaseWebsocketOutput: BaseWebsocketOutput{
			ws:   ws,
			mu:   sync.Mutex{},
			hbuf: hbuf,
			mediaFormat: MediaFormat{
				Encoding:   string(AudioEncodingLinear16),
				SampleRate: outputSamplingRate,
			},
			// shorter chunks = faster response time, but more jitter
			secondsPerChunk: .2,
			stopChan:        make(chan xid.ID),
			startedComps:    make(map[xid.ID]bool),
		},
		timingCache: timingCache,
		encoder:     encoder,
	}
	w.BaseWebsocketOutput.handleChunk = w.handleWebChunk
	return w
}

func (w *WebsocketOutput) handleWebChunk(
	ctx context.Context,
	chunk []byte,
) (msgJson any, err error) {
	encodedBytes, err := w.encoder.ConvertAudio(ctx, chunk)

	if err != nil {
		return msgJson, fmt.Errorf("error converting audio %w", err)
	}
	msgJson = AudioMessage{
		WebSocketMessage: WebSocketMessage{
			Type: WebSocketAudio,
		},
		Data: base64.StdEncoding.EncodeToString(encodedBytes),
	}
	return msgJson, nil
}

func (w *WebsocketOutput) Start(ctx context.Context, gazelleID xid.ID, synthReqID xid.ID) {
	if _, ok := w.startedComps[synthReqID]; ok {
		//logger.ErrorContext(ctx, "already started output for gazelleID", "gazelleID", gazelleID)
		return
	} else {
		w.startedComps[synthReqID] = true
	}
	w.timingCache.SetEndTime(gazelleID, int(time.Now().UnixNano()))

	w.BaseWebsocketOutput.start(ctx, gazelleID, synthReqID)
}

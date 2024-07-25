package gondola

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/xid"
	"github.com/stillmatic/gazelle-inference-demo/pkg/sentencesplit"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"github.com/stillmatic/gazelle-inference-demo/pkg/vad"
	"github.com/stillmatic/gazelle-inference-demo/pkg/wsw"
)

var logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))

// Component is a generic interface that exposes a consistent channel-based message-passing API.
type Component interface {
	Process(context.Context, <-chan types.GondolaMessage) <-chan types.GondolaMessage
}

// VADAccumulator listens to user audio and accumulates it until it detects a voice activity event.
type VADAccumulator struct {
	vadComp             Component
	vadInputChan        chan types.GondolaMessage
	vadOutputChan       <-chan types.GondolaMessage
	rawAccumulator      []byte
	accumulator         []byte
	voiceActive         bool
	voiceMutex          *sync.Mutex
	currID              xid.ID
	maxLengthWithoutVAD int
}

func (g *VADAccumulator) Process(ctx context.Context, input <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	output := make(chan types.GondolaMessage)

	go func() {
		defer close(output)

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-input:
				//	Process the message
				//		- accumulate audio
				//      - check for VAD
				//		- if we have voice and the voice has stopped, infer and return text
				//		- if we have voice and the voice has not stopped, continue accumulating audio
				//		- if we have no voice, reset the accumulator
				// 		- TODO: use a separate pause/endpoint detector here
				if msg.MessageType != types.MessageTypeRecordedAudio {
					logger.ErrorContext(ctx, "Received unexpected message type", "message", msg)
				}
				g.rawAccumulator = append(g.rawAccumulator, msg.Audio...)
				if g.voiceActive {
					// if this is empty, add the raw accumulator to it
					if len(g.accumulator) == 0 {
						g.accumulator = append(g.accumulator, g.rawAccumulator...)
					}
					g.accumulator = append(g.accumulator, msg.Audio...)
					// 2024-04-17: send incrementally to websocket
					go func() {
						output <- types.GondolaMessage{
							MessageType: types.MessageTypeRecordedAudio,
							EOF:         false,
							Audio:       msg.Audio,
							GazelleID:   g.currID,
						}
					}()
				}
				if len(g.rawAccumulator) > g.maxLengthWithoutVAD {
					//truncate to max length
					g.rawAccumulator = g.rawAccumulator[len(g.rawAccumulator)-g.maxLengthWithoutVAD:]
				}
				//	Process the audio
				g.vadInputChan <- msg
			case vadMsg := <-g.vadOutputChan:
				if vadMsg.Err != nil {
					logger.ErrorContext(ctx, "Error processing VAD", "error", vadMsg.Err)
					continue
				}
				//logger.Info("vad response", "message", vadMsg)
				// MessageTypeStopResponse is poorly named, it means 'voice detected, stop playing audio'
				// we will treat this as voice is now active!
				if vadMsg.MessageType == types.MessageTypeStopResponse {
					//g.voiceMutex.Lock()
					g.voiceActive = true
					g.currID = xid.New()
					//g.voiceMutex.Unlock()
				} else if vadMsg.MessageType == types.MessageTypeSendResponse {
					//g.voiceMutex.Lock()
					g.voiceActive = false
					//g.voiceMutex.Unlock()
					// send the accumulated audio to the next component
					output <- types.GondolaMessage{
						MessageType: types.MessageTypeRecordedAudio,
						EOF:         true,
						GazelleID:   g.currID,
						//Audio:       g.accumulator,
					}
					g.accumulator = []byte{}
				} else {
					logger.ErrorContext(ctx, "Received unexpected message type", "message", vadMsg)
				}
			}
		}
	}()

	return output
}

func NewVADAccumulator(ctx context.Context, samplingRate int) (*VADAccumulator, error) {
	vadComp, err := vad.NewSileroVAD(ctx, "ws://localhost:80/detect-voice-activity", samplingRate)
	if err != nil {
		return nil, err
	}
	vadInputChan := make(chan types.GondolaMessage)
	vadOutputChan := vadComp.Process(ctx, vadInputChan)
	return &VADAccumulator{
		vadComp:        vadComp,
		vadInputChan:   vadInputChan,
		vadOutputChan:  vadOutputChan,
		rawAccumulator: []byte{},
		accumulator:    []byte{},
		//arbitrary
		maxLengthWithoutVAD: 16000,
		voiceActive:         false,
		voiceMutex:          &sync.Mutex{},
	}, nil
}

type GazelleClient struct {
	URL           string
	splitter      *sentencesplit.SentenceSplitter
	client        *http.Client
	seenSentences map[string]bool
	currID        xid.ID
}

func NewGazelleClient(url string) *GazelleClient {
	return &GazelleClient{
		URL:      url,
		splitter: sentencesplit.NewSentenceSplitter(),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		seenSentences: make(map[string]bool),
	}
}

func (c *GazelleClient) SetCurrID(id xid.ID) {
	c.currID = id
}

// Process opens a websocket and streams audio to the server.
// Infer instead does a single synchronous call to the server.
func (c *GazelleClient) Process(ctx context.Context, input <-chan types.GondolaMessage) <-chan types.GondolaMessage {
	output := make(chan types.GondolaMessage)

	// hit websocket, c.URL+"/audio"
	dialer, _, err := websocket.DefaultDialer.Dial("ws://localhost:8082/audio", nil)
	if err != nil {
		logger.ErrorContext(ctx, "Error dialing websocket", "error", err)
		return nil
	}
	// TODO: if we know the max size of byte representation of audio, we can preallocate
	bb := &bytes.Buffer{}
	b64Encoder := base64.NewEncoder(base64.StdEncoding, bb)

	go func() {
		defer b64Encoder.Close()

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-input:
				if msg.MessageType != types.MessageTypeRecordedAudio {
					logger.ErrorContext(ctx, "Received unexpected message type", "message", msg)
				}
				if msg.EOF {
					err = dialer.WriteMessage(websocket.TextMessage, []byte("EOF"))
					if err != nil {
						logger.ErrorContext(ctx, "Error writing EOF to websocket", "error", err)
					}
					continue
				}

				_, err = b64Encoder.Write(msg.Audio)
				if err != nil {
					logger.ErrorContext(ctx, "Error writing audio to base64", "error", err)
				}
				err = dialer.WriteMessage(websocket.TextMessage, bb.Bytes())
				if err != nil {
					logger.ErrorContext(ctx, "Error writing message to websocket", "error", err)
				}
				bb.Reset()
			}
		}
	}()
	//  read from dialer and send to output
	go func() {
		// defer close(output)
		for {
			_, message, err := dialer.ReadMessage()
			if err != nil {
				logger.ErrorContext(ctx, "Error reading message from websocket", "error", err)
				return
			}
			messageStr := string(message)
			if messageStr == "</s>" {
				output <- types.GondolaMessage{
					MessageType: types.MessageTypeLanguageModelerOutput,
					EOF:         true,
					Timestamp:   time.Now(),
					GazelleID:   c.currID,
				}
				continue
			} else if messageStr == "" {
				continue
			}
			output <- types.GondolaMessage{
				Content:     messageStr,
				MessageType: types.MessageTypeLanguageModelerOutput,
				Err:         nil,
				Timestamp:   time.Now(),
				GazelleID:   c.currID,
			}
		}
	}()

	return output
}

// createRequest creates a new HTTP request with the given input and audio file.
func (c *GazelleClient) createRequest(input string, audioFilePath string, compress bool) (*http.Request, error) {
	// Create a new multipart writer
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create the "input" field
	inputField, err := writer.CreateFormField("input")
	if err != nil {
		return nil, fmt.Errorf("error creating input field: %v", err)
	}
	_, err = inputField.Write([]byte(input))
	if err != nil {
		return nil, fmt.Errorf("error writing input field: %v", err)
	}

	// Create the "audio" field
	audioFile, err := os.Open(audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("error opening audio file: %v", err)
	}
	defer audioFile.Close()

	audioField, err := writer.CreateFormFile("audio", audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("error creating audio field: %v", err)
	}
	_, err = io.Copy(audioField, audioFile)
	if err != nil {
		return nil, fmt.Errorf("error copying audio file: %v", err)
	}

	// Close the multipart writer
	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("error closing multipart writer: %v", err)
	}

	// Create a new HTTP request
	req, err := http.NewRequest("POST", c.URL, body)
	if err != nil {
		return nil, fmt.Errorf("error creating request: %v", err)
	}

	// Set the Content-Type header
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return req, nil
}

// Infer sends the given input and audio file to the Gazelle server and sends the response to the given channel.
func (c *GazelleClient) Infer(input string, audioFilePath string, outputChan chan<- types.GondolaMessage, gazelleID xid.ID) error {
	t0 := time.Now()
	req, err := c.createRequest(input, audioFilePath, false)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	// Send the request
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	// Read response body as it streams in
	sb := &strings.Builder{}
	buffer := make([]byte, 1024)
	ttft := 0
	for {
		n, err := resp.Body.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("error reading response body: %v", err)
		}
		if ttft == 0 {
			ttft = int(time.Since(t0).Milliseconds())
			logger.Info("time to first token", "milliseconds", ttft)
		}
		sb.Write(buffer[:n])
		output := sb.String()
		lowerOutput := strings.ToLower(output)
		if c.seenSentences[lowerOutput] {
			continue
		}
		// with current gazelle inference server, output is a single sentence
		outputChan <- types.GondolaMessage{
			Content:     output,
			MessageType: types.MessageTypeLanguageModelerOutput,
			Err:         nil,
			Timestamp:   time.Now(),
			GazelleID:   gazelleID,
		}

		c.seenSentences[lowerOutput] = true
		sb.Reset()

	}
	if sb.Len() > 0 {
		outputChan <- types.GondolaMessage{
			Content:     sb.String(),
			MessageType: types.MessageTypeLanguageModelerOutput,
			EOF:         true,
			Err:         nil,
			Timestamp:   time.Now(),
			GazelleID:   gazelleID,
		}
	} else {
		outputChan <- types.GondolaMessage{
			Content:     sb.String(),
			MessageType: types.MessageTypeLanguageModelerOutput,
			EOF:         true,
			Err:         nil,
			Timestamp:   time.Now(),
			GazelleID:   gazelleID,
		}
	}

	return nil
}

type TimingCacheEntry struct {
	startTime *int
	endTime   *int
}

// TimingCache stores a map of start time and end time for each message ID.
// This is a quick and dirty way to track time to first byte.
type TimingCache struct {
	timings map[xid.ID]TimingCacheEntry
	mu      *sync.Mutex
}

func NewTimingCache() *TimingCache {
	return &TimingCache{
		timings: make(map[xid.ID]TimingCacheEntry),
		mu:      &sync.Mutex{},
	}
}

func (t *TimingCache) SetStartTime(id xid.ID, time int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.timings[id]
	if ok {
		return
	}
	entry = TimingCacheEntry{
		startTime: &time,
	}
	t.timings[id] = entry
	logger.Info("mark start", "id", id)
}

func (t *TimingCache) SetEndTime(id xid.ID, time int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.timings[id]
	if !ok {
		logger.Warn("tried to set end time for non-existent ID", "id", id)
		return
	}
	if entry.endTime != nil {
		return
	}
	entry.endTime = &time
	t.timings[id] = entry

	// this is nanoseconds
	delta := t.GetTiming(id)
	deltaMs := delta / 1000000
	logger.Info("time to first output", "id", id, "delta_ms", deltaMs)
}

func (t *TimingCache) GetStartTime(id xid.ID) int {
	entry, ok := t.timings[id]
	if !ok {
		logger.Warn("tried to get start time for non-existent ID", "id", id)
		return -1
	}
	if entry.startTime == nil {
		logger.Warn("tried to get start time for ID without start time", "id", id)
		return -1
	}
	return *entry.startTime
}

func (t *TimingCache) GetTiming(id xid.ID) int {
	entry, ok := t.timings[id]
	if !ok {
		logger.Warn("tried to get timing for non-existent ID", "id", id)
		return -1
	}
	if entry.startTime == nil || entry.endTime == nil {
		logger.Warn("tried to get timing for ID without both start and end time", "id", id)
		return -1
	}
	return *entry.endTime - *entry.startTime
}

type Orchestrator struct {
	inputComp     WebsocketInputStreamer
	outputComp    WebsocketOutputter
	vadAccComp    Component
	startMsg      AudioConfigStartMessage
	gazelleClient *GazelleClient
	synthComp     Component
	timingCache   *TimingCache
}

func NewOrchestrator(startMsg AudioConfigStartMessage, ws *wsw.WSWrapper) (*Orchestrator, error) {
	ctx := context.Background()

	inputComp := NewWebsocketInput(ws)
	vadAccComp, err := NewVADAccumulator(ctx, startMsg.InputAudioConfig.SamplingRate)
	if err != nil {
		return nil, err
	}
	gazelleClient := NewGazelleClient("http://localhost:8082/generate")

	//read from env
	playHTID := os.Getenv("PLAYHT_ID")
	playHTSecret := os.Getenv("PLAYHT_SECRET")
	playHTCfg := PlayHTConfig{
		ID:           playHTID,
		Secret:       playHTSecret,
		SamplingRate: 24000,
		OutputFormat: "raw",
	}
	playHTSynth, err := NewPlayHTSynthesizer(playHTCfg)
	if err != nil {
		return nil, err
	}

	timingCache := NewTimingCache()
	websocketOutputter := NewWebsocketOutput(ctx, ws, 24000, timingCache)

	return &Orchestrator{
		inputComp:     inputComp,
		outputComp:    websocketOutputter,
		vadAccComp:    vadAccComp,
		startMsg:      startMsg,
		gazelleClient: gazelleClient,
		synthComp:     playHTSynth,
		timingCache:   timingCache,
	}, nil
}

func randomString(length int) string {
	b := make([]byte, length+2)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)[2 : length+2]
}

func (o *Orchestrator) Start(ctx context.Context) error {
	logger.Info("starting orchestrator")

	inputChan := o.inputComp.Stream(ctx)
	vadInputChan := make(chan types.GondolaMessage)

	vadOutputChan := o.vadAccComp.Process(ctx, vadInputChan)
	gazelleInputChan := make(chan types.GondolaMessage)
	gazelleOutChan := o.gazelleClient.Process(ctx, gazelleInputChan)

	//gazelleOutputChan := make(chan types.GondolaMessage)
	synthInputChan := make(chan types.GondolaMessage)
	synthOutputChan := o.synthComp.Process(ctx, synthInputChan)

	outputInputChan := make(chan types.GondolaMessage)
	outputErrChan := o.outputComp.Sink(ctx, outputInputChan)

	//wavEnc := wavencoder.NewWavEncoder(o.startMsg.InputAudioConfig.SamplingRate, 16, 1, 1)
	logger.Info("set up orchestrator components")
	go func() {
		defer close(vadInputChan)
		//defer close(gazelleOutputChan)
		defer close(synthInputChan)
		defer close(outputInputChan)

		for {
			select {
			case <-ctx.Done():

				return
			case msg := <-inputChan:
				// This channel takes audio in and sends to VAD
				if msg.MessageType != types.MessageTypeRecordedAudio {
					logger.ErrorContext(ctx, "Received unexpected message type", "message", msg)
					continue
				}
				//	forward to next component (gazelle)
				vadInputChan <- msg
			case msg := <-vadOutputChan:
				// This channel takes accumulated segments from VAD and sends to Gazelle
				// The next component receives the fully formed sentences.
				// mark
				// logger.Info("Received message from vad", "audioLen", len(msg.Audio))

				// start timing when user finishes talking
				if msg.EOF {
					//logger.Info("Sending start time to cache", "gazelleID", msg.GazelleID)
					o.timingCache.SetStartTime(msg.GazelleID, int(time.Now().UnixNano()))
					// THIS WILL NOT WORK WITH INTERRUPTIONS
					o.gazelleClient.SetCurrID(msg.GazelleID)
				}

				// send the audio to the gazelle client
				gazelleInputChan <- msg
			case msg := <-gazelleOutChan:
				// This channel receives the text response from Gazelle
				// At this point, we do TTS and send the audio back to the user
				startTime := o.timingCache.GetStartTime(msg.GazelleID)
				delta := int(time.Now().UnixNano()) - startTime
				deltaMs := delta / 1000000
				logger.Info("received text response", "content", msg.Content, "eof", msg.EOF, "delta_ms", deltaMs, "gazelleID", msg.GazelleID)
				if msg.EOF {
					// set the synthesizer EOF in the output comp
					o.outputComp.SetSynthesizerEOF(msg.GazelleID, msg.SynthesizerID)
					continue
				}

				trimmedStr := strings.TrimSpace(strings.ReplaceAll(msg.Content, "\n", ""))
				if trimmedStr == "" {
					continue
				}
				msg.Content = trimmedStr
				msg.SynthesizerID = xid.New()
				// send the text to the synthesizer
				go func() {
					synthInputChan <- msg
					logger.Info("sent to synth", "content", msg.Content)
				}()
			case msg := <-synthOutputChan:
				// This channel receives the audio response from the synthesizer
				// At this point, we send the audio back to the user
				// send the audio to the output component
				outputInputChan <- msg
				go func() {
					o.outputComp.Start(ctx, msg.GazelleID, msg.SynthesizerID)
				}()
			case err := <-outputErrChan:
				if err != nil {
					logger.ErrorContext(ctx, "Error from output component", "error", err)
				}
			}
		}
	}()
	return nil
}

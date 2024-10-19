package gondola

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/rs/xid"
	"github.com/stillmatic/gazelle-inference-demo/pkg/sentencesplit"
	"github.com/stillmatic/gazelle-inference-demo/pkg/types"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

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

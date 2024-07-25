package gondola

// enums

type AudioEncoding string
type WebSocketMessageType string

const (
	AudioEncodingLinear16 AudioEncoding = "linear16"
	AudioEncodingMulaw    AudioEncoding = "mulaw"
)

const (
	// WebSocketStart is not supported by this service.
	WebSocketStart            WebSocketMessageType = "websocket_start"
	WebSocketAudio            WebSocketMessageType = "websocket_audio"
	WebSocketTranscript       WebSocketMessageType = "websocket_transcript"
	WebSocketReady            WebSocketMessageType = "websocket_ready"
	WebSocketStop             WebSocketMessageType = "websocket_stop"
	WebSocketAudioConfigStart WebSocketMessageType = "websocket_audio_config_start"
)

type WebSocketMessage struct {
	Type WebSocketMessageType `json:"type"`
}

func (m *WebSocketMessage) GetWebSocketMessageType() WebSocketMessageType {
	return m.Type
}

type WebSocketMessager interface {
	GetWebSocketMessageType() WebSocketMessageType
}

type InputAudioConfig struct {
	SamplingRate  int           `json:"sampling_rate"`
	AudioEncoding AudioEncoding `json:"audio_encoding"`
	ChunkSize     int           `json:"chunk_size"`
	Downsampling  *int          `json:"downsampling,omitempty"`
}

func (m *InputAudioConfig) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketAudioConfigStart
}

type OutputAudioConfig struct {
	SamplingRate  int           `json:"sampling_rate"`
	AudioEncoding AudioEncoding `json:"audio_encoding"`
}

func (m *OutputAudioConfig) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketAudioConfigStart
}

type AudioConfigStartMessage struct {
	WebSocketMessage
	InputAudioConfig    InputAudioConfig  `json:"input_audio_config"`
	OutputAudioConfig   OutputAudioConfig `json:"output_audio_config"`
	ConversationID      *string           `json:"conversation_id,omitempty"`
	SubscribeTranscript *bool             `json:"subscribe_transcript,omitempty"`

	FirstMessage *string `json:"first_message,omitempty"`
	SystemPrompt *string `json:"system_prompt,omitempty"`
}

func (m *AudioConfigStartMessage) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketAudioConfigStart
}

type AudioMessage struct {
	WebSocketMessage
	Data string `json:"data"`
}

func (m *AudioMessage) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketAudio
}

type TranscriptMessage struct {
	WebSocketMessage
	Text      string `json:"text"`
	Sender    string `json:"sender"`
	Timestamp string `json:"timestamp"`
}

func (m *TranscriptMessage) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketTranscript
}

type ReadyMessage struct {
	WebSocketMessage
}

func (m *ReadyMessage) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketReady
}

type StopMessage struct {
	WebSocketMessage
}

func (m *StopMessage) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketStop
}

type SelfHostedConversationConfig struct {
	BackendURL          string
	AudioDeviceConfig   AudioDeviceConfig
	ConversationID      *string
	TimeSlice           *int
	ChunkSize           *int
	Downsampling        *int
	SubscribeTranscript *bool
}

func (m *SelfHostedConversationConfig) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketAudioConfigStart
}

type AudioDeviceConfig struct {
	InputDeviceID      *string
	OutputDeviceID     *string
	OutputSamplingRate *int
}

func (m *AudioDeviceConfig) GetWebSocketMessageType() WebSocketMessageType {
	return WebSocketAudioConfigStart
}

// Twilio structs

type TwilioEvent struct {
	Event          string       `json:"event"`
	SequenceNumber string       `json:"sequenceNumber,omitempty"`
	Media          *TwilioMedia `json:"media,omitempty"`
	Start          *TwilioStart `json:"start,omitempty"`
	StreamSid      string       `json:"streamSid,omitempty"`
	Protocol       string       `json:"protocol,omitempty"`
	Version        string       `json:"version,omitempty"`
}

type TwilioMedia struct {
	Track     string `json:"track"`
	Chunk     string `json:"chunk"`
	Timestamp string `json:"timestamp"`
	Payload   string `json:"payload"`
}

type TwilioStart struct {
	AccountSid       string            `json:"accountSid"`
	CallSid          string            `json:"callSid"`
	Tracks           []string          `json:"tracks"`
	MediaFormat      MediaFormat       `json:"mediaFormat"`
	CustomParameters map[string]string `json:"customParameters"`
}

type CustomParameter struct {
	CallID *string `json:"CallID"`
}

type MediaFormat struct {
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sampleRate"`
	Channels   int    `json:"channels"`
}

type TwilioOutEvent struct {
	Event     string          `json:"event"`
	StreamSid string          `json:"streamSid,omitempty"`
	Media     *TwilioOutMedia `json:"media,omitempty"`
}

type TwilioOutMedia struct {
	Payload string `json:"payload"`
}

type TwilioAMDEvent struct {
	CallSid                  string `json:"callSid"`
	AccountSid               string `json:"accountSid"`
	AnsweredBy               string `json:"answeredBy"`
	MachineDetectionDuration int    `json:"machineDetectionDuration"`
}

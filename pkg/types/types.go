package types

import (
	"github.com/rs/xid"
	"time"
)

// MessageType is the type of message that is passed between the components of
// the gossip pipeline.
type MessageType int

const (
	// MessageTypeRecordedAudio is the message type that is passed from the audio recorder.
	MessageTypeRecordedAudio MessageType = iota
	// MessageTypeLanguageModelerOutput is the message type that is passed from the language modeler.
	MessageTypeLanguageModelerOutput
	// MessageTypeSynthesizerOutput is the message type that is passed from the synthesizer.
	MessageTypeSynthesizerOutput
	// MessageTypeSendResponse means to start playing audio to the user when ready (i.e. user is done)
	MessageTypeSendResponse
	// MessageTypeStopResponse means to stop playing audio to the user (i.e. has been interrupted)
	MessageTypeStopResponse
	// MessageTypeBotUtteredMessage is a message that the bot has uttered.
	MessageTypeBotUtteredMessage
	//	MessageTypeShouldEndCall is a signal that the call should end.
	MessageTypeShouldEndCall
)

// GondolaMessage is a shared interface for all messages passed between components.
type GondolaMessage struct {
	Audio        []byte
	SamplingRate *int

	History []ConversationMessage
	Content string

	// EOF is used in several steps to indicate that this is the last output for that sequence.
	// In the language modeler step, it indicates that there is no more reply after that sequence.
	EOF bool

	MessageType   MessageType
	GazelleID     xid.ID
	SynthesizerID xid.ID

	Err       error
	Timestamp time.Time
}

// ConversationMessage is like openai.ChatCompletionMessage but with an Audio field
type ConversationMessage struct {
	Content string
	Role    string
	Audio   []byte
}

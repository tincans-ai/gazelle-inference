package sentencesplit

import (
	"strings"

	"github.com/neurosnap/sentences"
	"github.com/neurosnap/sentences/english"
)

type SentenceSplitter struct {
	tokenizer *sentences.DefaultSentenceTokenizer
}

func NewSentenceSplitter() *SentenceSplitter {
	tokenizer, err := english.NewSentenceTokenizer(nil)
	if err != nil {
		panic(err)
	}
	return &SentenceSplitter{
		tokenizer: tokenizer,
	}
}

// SplitSentence takes an arbitrary chunk of text and splits it into a sentence and remainder.
// If there is a fully formed sentence AND additional text, it returns chunk plus the remainder.
// It's possible there's only one sentence.
func (ss *SentenceSplitter) SplitSentence(input string) (chunk string, remainder string) {
	sentences_ := ss.tokenizer.Tokenize(input)
	if len(sentences_) == 0 {
		return "", input
	}
	if len(sentences_) == 1 {
		return sentences_[0].Text, ""
	}
	return sentences_[0].Text, strings.TrimSpace(input[sentences_[0].End:])
}

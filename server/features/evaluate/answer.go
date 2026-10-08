package evaluate

import (
	"github.com/hasan-muzak/keel-core/pkg/llm/wire"

	"github.com/hasan-muzak/promptdam/server/features/answers"
)

// Answer is what a model answered, as the checks and the judge compare
// it.
type Answer = answers.Answer

// ToolCall is a tool an answer called.
type ToolCall = answers.ToolCall

// answerOf reads an answer of format f, whole or streamed.
func answerOf(f wire.Format, body []byte, stream bool) (Answer, error) {
	return answers.Of(f, body, stream)
}

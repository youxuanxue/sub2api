package service

import "github.com/gin-gonic/gin"

// Content-policy blocks are complete request-scoped results, including prompt
// blocks with no candidates. Detection and attribution remain native owners.
func geminiCompatPolicySignal(payload []byte) (geminiResponseSignal, bool) {
	signal, ok := detectGeminiResponseSignal(payload)
	return signal, ok && (signal.Kind == geminiSignalPromptBlocked || signal.Kind == geminiSignalContentFilter)
}

func (s *GeminiMessagesCompatService) markGeminiCompatPolicySignal(c *gin.Context, payload []byte, stream bool) bool {
	signal, ok := geminiCompatPolicySignal(payload)
	if ok {
		s.markGeminiResponseSignal(c, nil, signal, stream, "")
	}
	return ok
}

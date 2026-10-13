package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// openAIImagesOAuthDispatchFacts is the OAuth / Codex Responses image dispatch
// that edge-uk1 could not see on 2026-10-13: usage_logs effort columns were
// NULL, and main_model / scaffold had no column and no log field.
//
// usage_logs already has reasoning_effort and requested_reasoning_effort.
// main_model and scaffold_on do not; those stay on the dispatch log.
// The Images API has no client reasoning_effort field, so the recorded effort
// is the value TokenKey writes to reasoning.effort (env override or the
// existing auto tier). Requested and effective columns therefore match.
type openAIImagesOAuthDispatchFacts struct {
	ReasoningEffort      string
	EffortSource         string
	MainModel            string
	MainModelFallbacks   []string
	ScaffoldOn           bool
	ToolModel            string
	ClientInputFidelity  string
	InputFidelityOmitted bool
}

// openAIImagesCodexSoftModelRejectsInputFidelity reports image tool models that
// ChatGPT Codex Responses executes as gpt-image-2-codex.
//
// edge-uk1 (2026-10-13) sent edits with input_fidelity=high and low on
// model=gpt-image-2. Upstream answered 400 invalid_input_fidelity_model:
// model gpt-image-2-codex does not support input_fidelity. Omitting the field
// returned 200. gpt-image-2 is the tool name that soft-serves as that model;
// gpt-image-2-codex is the name in the error. gpt-image-2.5-* and gpt-image-1*
// are not that soft model: the Responses builder still forwards fidelity, and
// the API-key Images passthrough never calls this predicate.
func openAIImagesCodexSoftModelRejectsInputFidelity(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model == "gpt-image-2" || model == "gpt-image-2-codex" || strings.HasPrefix(model, "gpt-image-2-codex-")
}

func openAIImagesClientPrompt(parsed *OpenAIImagesRequest) string {
	if parsed == nil {
		return ""
	}
	prompt := parsed.Prompt
	if !parsed.Multipart && gjson.ValidBytes(parsed.Body) {
		if rawPrompt := gjson.GetBytes(parsed.Body, "prompt").String(); rawPrompt != "" {
			prompt = rawPrompt
		}
	}
	return prompt
}

func openAIImagesMainModelFallbacksAfter(current string) []string {
	currentKey := strings.ToLower(strings.TrimSpace(current))
	candidates := openAIImagesMainModelCandidates()
	found := -1
	for i, candidate := range candidates {
		if strings.ToLower(candidate) == currentKey {
			found = i
			break
		}
	}
	rest := candidates
	if found >= 0 {
		rest = candidates[found+1:]
	}
	out := make([]string, 0, len(rest))
	for _, candidate := range rest {
		if strings.ToLower(candidate) == currentKey {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func openAIImagesOAuthDispatchFactsFor(parsed *OpenAIImagesRequest, toolModel, mainModel string) openAIImagesOAuthDispatchFacts {
	prompt := openAIImagesClientPrompt(parsed)
	toolModel = strings.TrimSpace(toolModel)
	mainModel = strings.TrimSpace(mainModel)
	if mainModel == "" {
		mainModel = openAIImagesResponsesMainModelValue()
	}
	fidelity := ""
	if parsed != nil {
		fidelity = strings.TrimSpace(parsed.InputFidelity)
	}
	source := "auto"
	if _, ok := openAIImagesExplicitReasoningEffort(); ok {
		source = "env"
	}
	return openAIImagesOAuthDispatchFacts{
		ReasoningEffort:      openAIImagesReasoningEffortForPrompt(prompt),
		EffortSource:         source,
		MainModel:            mainModel,
		MainModelFallbacks:   openAIImagesMainModelFallbacksAfter(mainModel),
		ScaffoldOn:           openAIImagesPromptScaffoldEnabled(),
		ToolModel:            toolModel,
		ClientInputFidelity:  fidelity,
		InputFidelityOmitted: fidelity != "" && openAIImagesCodexSoftModelRejectsInputFidelity(toolModel),
	}
}

func logOpenAIImagesOAuthDispatch(account *Account, requestModel, endpoint string, uploads int, facts openAIImagesOAuthDispatchFacts) {
	accountID := int64(0)
	accountType := ""
	if account != nil {
		accountID = account.ID
		accountType = account.Type
	}
	fallbacks := strings.Join(facts.MainModelFallbacks, ",")
	logger.LegacyPrintf(
		"service.openai_gateway",
		"[OpenAI] Images request routing request_model=%s endpoint=%s account_type=%s uploads=%d reasoning_effort=%s effort_source=%s main_model=%s main_model_fallbacks=%s scaffold_on=%t tool_model=%s input_fidelity_omitted=%t",
		requestModel,
		endpoint,
		accountType,
		uploads,
		facts.ReasoningEffort,
		facts.EffortSource,
		facts.MainModel,
		fallbacks,
		facts.ScaffoldOn,
		facts.ToolModel,
		facts.InputFidelityOmitted,
	)
	logger.L().Info("openai_images.oauth_dispatch",
		zap.Int64("account_id", accountID),
		zap.String("request_model", strings.TrimSpace(requestModel)),
		zap.String("endpoint", endpoint),
		zap.String("account_type", accountType),
		zap.Int("uploads", uploads),
		zap.String("reasoning_effort", facts.ReasoningEffort),
		zap.String("effort_source", facts.EffortSource),
		zap.String("main_model", facts.MainModel),
		zap.Strings("main_model_fallbacks", facts.MainModelFallbacks),
		zap.Bool("scaffold_on", facts.ScaffoldOn),
		zap.String("tool_model", facts.ToolModel),
		zap.String("client_input_fidelity", facts.ClientInputFidelity),
		zap.Bool("input_fidelity_omitted", facts.InputFidelityOmitted),
	)
}

func stampOpenAIImagesOAuthDispatch(result *OpenAIForwardResult, facts openAIImagesOAuthDispatchFacts) {
	if result == nil {
		return
	}
	if effort := strings.TrimSpace(facts.ReasoningEffort); effort != "" {
		result.ReasoningEffort = &effort
	}
}

package bridge

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	newapirelay "github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// ResolveTextEndpoint asks the live New API adaptor for the exact URL it would
// use for a text protocol. It performs no network I/O and is shared by planning
// and execution so provider-specific paths are not copied into TokenKey.
func ResolveTextEndpoint(in ChannelContextInput, relayFormat types.RelayFormat, model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("model is required")
	}
	path := "/v1/chat/completions"
	var request dto.Request = &dto.GeneralOpenAIRequest{Model: model}
	if relayFormat == types.RelayFormatOpenAIResponses {
		path = "/v1/responses"
		request = &dto.OpenAIResponsesRequest{Model: model}
	} else if relayFormat != types.RelayFormatOpenAI {
		return "", fmt.Errorf("unsupported text relay format %q", relayFormat)
	}

	c, _ := gin.CreateTestContext(nil)
	c.Request = httptest.NewRequest("POST", path, nil)
	PopulateContextKeys(c, in)
	SetOriginalModel(c, model)
	relayInfo, err := relaycommon.GenRelayInfo(c, relayFormat, request, nil)
	if err != nil {
		return "", err
	}
	relayInfo.InitChannelMeta(c)
	if err := helper.ModelMappedHelper(c, relayInfo, request); err != nil {
		return "", err
	}
	adaptor := newapirelay.GetAdaptor(relayInfo.ApiType)
	if adaptor == nil {
		return "", fmt.Errorf("invalid api type %d", relayInfo.ApiType)
	}
	adaptor.Init(relayInfo)
	endpoint, err := adaptor.GetRequestURL(relayInfo)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(endpoint), nil
}

// WithoutModelMapping returns a copy that prevents the adaptor from applying a
// second account mapping after the protocol plan has resolved the upstream model.
func (in ChannelContextInput) WithoutModelMapping() ChannelContextInput {
	in.ModelMappingJSON = ""
	return in
}

// Keep encoding/json referenced by old new-api DTO variants whose Request
// interface methods are generated behind build tags.
var _ = json.RawMessage{}

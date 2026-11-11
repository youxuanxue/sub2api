package bridge

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapichannel "github.com/QuantumNous/new-api/relay/channel"
	taskdoubao "github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	newapiservice "github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// TokenKey: VolcEngine Agent Plan video task adaptor.
//
// Plan accounts store the OpenAI-compatible root
// https://ark.cn-beijing.volces.com/api/plan/v3 because chat and responses
// append /chat/completions and /responses to it. The upstream doubao task
// adaptor assumes that root is only the Ark host and always appends
// /api/v3/contents/generations/tasks. On a plan account that becomes
// /api/plan/v3/api/v3/contents/generations/tasks, which Ark answers with an
// empty 404. The same plan key accepts
// {plan root}/contents/generations/tasks and is rejected with 401 on the
// pay-as-you-go /api/v3 path, so the stored base must not be stripped back to
// the host. new-api stays unmodified; this wrapper only replaces the two URLs.
type volcenginePlanTaskAdaptor struct {
	*taskdoubao.TaskAdaptor

	// baseURL mirrors what Init received. The embedded adaptor keeps its own
	// copy in an unexported field, so this wrapper captures the normalized
	// plan root itself.
	baseURL string
}

func newVolcenginePlanTaskAdaptor() *volcenginePlanTaskAdaptor {
	return &volcenginePlanTaskAdaptor{TaskAdaptor: &taskdoubao.TaskAdaptor{}}
}

func (a *volcenginePlanTaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info != nil {
		a.baseURL = volcenginePlanVideoBase(info.ChannelBaseUrl)
	}
	a.TaskAdaptor.Init(info)
}

func (a *volcenginePlanTaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return volcenginePlanContentsURL(a.baseURL, "")
}

// DoRequest preserves method dispatch to this wrapper's BuildRequestURL.
// The promoted TaskAdaptor.DoRequest would pass the embedded receiver to
// DoTaskApiRequest and silently restore /api/v3/.
func (a *volcenginePlanTaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return newapichannel.DoTaskApiRequest(a, c, info, requestBody)
}

func (a *volcenginePlanTaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("invalid task_id")
	}
	uri, err := volcenginePlanContentsURL(baseUrl, taskID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client, err := newapiservice.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("volcengine plan video fetch: new proxy http client failed: %w", err)
	}
	return client.Do(req)
}

func volcenginePlanVideoBase(base string) string {
	return strings.TrimRight(newapiintegration.NormalizeArkChannelBaseURL(newapiconstant.ChannelTypeVolcEngine, base), "/")
}

func volcenginePlanContentsURL(base, taskID string) (string, error) {
	root := volcenginePlanVideoBase(base)
	if root == "" {
		return "", fmt.Errorf("volcengine plan video: empty base_url")
	}
	uri := root + "/contents/generations/tasks"
	if taskID != "" {
		uri += "/" + taskID
	}
	return uri, nil
}

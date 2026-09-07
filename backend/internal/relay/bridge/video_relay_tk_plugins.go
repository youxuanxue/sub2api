package bridge

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func newArkTaskAdaptor() channel.TaskAdaptor {
	return relay.GetTaskAdaptor(constant.TaskPlatform(strconv.Itoa(constant.ChannelTypeDoubaoVideo)))
}

func setArkTaskRequestHeaders(req *http.Request, info *relaycommon.RelayInfo) error {
	if info == nil || info.ChannelMeta == nil || info.ApiKey == "" {
		return fmt.Errorf("missing task channel credential")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+info.ApiKey)
	return nil
}

// The new upstream polling contract accepts a task snapshot. This is a value
// object only; TokenKey remains the persistence owner and never invokes GORM.
func newAPIVideoPollTask(in VideoFetchInput) *model.Task {
	return &model.Task{
		TaskID:      in.UpstreamTaskID,
		Data:        in.TaskData,
		Properties:  model.Properties{OriginModelName: in.OriginModel, UpstreamModelName: in.OriginModel},
		PrivateData: model.TaskPrivateData{UpstreamTaskID: in.UpstreamTaskID, Key: in.APIKey, PluginState: in.PluginState},
	}
}

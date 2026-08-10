package service

import (
	"strings"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

const (
	VideoBillingResolution480P  = newapiintegration.VideoTaskResolution480P
	VideoBillingResolution720P  = newapiintegration.VideoTaskResolution720P
	VideoBillingResolution1080P = newapiintegration.VideoTaskResolution1080P
	VideoBillingResolution4K    = newapiintegration.VideoTaskResolution4K
)

// xAI 视频生成按秒计费，duration 请求参数允许 1-15 秒；未指定时上游默认生成 8 秒。
// 计费时长必须与上游实际消耗对齐，否则用户可通过拉长 duration 套利（提交时长由用户控制）。
const (
	VideoBillingMinDurationSeconds     = 1
	VideoBillingMaxDurationSeconds     = 15
	VideoBillingDefaultDurationSeconds = 8
)

// NormalizeVideoBillingDurationSecondsOrDefault 归一化计费用视频时长：
// 未指定（<=0）按上游默认 8 秒计，超出上游允许区间按边界收敛。
func NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds int) int {
	if durationSeconds <= 0 {
		return VideoBillingDefaultDurationSeconds
	}
	if durationSeconds < VideoBillingMinDurationSeconds {
		return VideoBillingMinDurationSeconds
	}
	if durationSeconds > VideoBillingMaxDurationSeconds {
		return VideoBillingMaxDurationSeconds
	}
	return durationSeconds
}

func NormalizeVideoBillingResolutionOrDefault(resolution string) string {
	if normalized, ok := newapiintegration.NormalizeVideoTaskResolution(resolution); ok {
		return normalized
	}
	return VideoBillingResolution480P
}

// NormalizeVideoBillingResolutionForModel applies model-specific defaults when resolution is empty
// and clamps unsupported tiers to the model default (official alignment).
func NormalizeVideoBillingResolutionForModel(model, resolution string) string {
	if strings.TrimSpace(resolution) == "" {
		return tkVideoDefaultResolution(model)
	}
	return tkVideoNormalizeResolution(model, resolution)
}

// NormalizeVideoBillingResolutionOrDefault 用于运行时计费：上游回传的分辨率
// 缺失或无法识别时按最低档兜底，保证请求仍可计费。
func NormalizeVideoBillingResolutionOrDefault(resolution string) string {
	if normalized, ok := LookupVideoBillingResolution(resolution); ok {
		return normalized
	}
	return VideoBillingResolution480P
}

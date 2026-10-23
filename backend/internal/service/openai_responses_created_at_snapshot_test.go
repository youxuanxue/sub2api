package service

import "regexp"

// withoutStampedCreatedAt 去掉网关补上的 response.created_at，让分帧快照
// 继续比较事件边界。这些快照的上游原文里没有该字段；盖章行为由
// openai_responses_created_at_test.go 单独锁定。
var stampedCreatedAtPattern = regexp.MustCompile(`,"created_at":\d+`)

func withoutStampedCreatedAt(s string) string {
	return stampedCreatedAtPattern.ReplaceAllString(s, "")
}

func stampedCreatedAtWidth(s string) int {
	return len(stampedCreatedAtPattern.FindString(s))
}

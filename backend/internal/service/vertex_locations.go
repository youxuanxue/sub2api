package service

import (
	"strings"
)

const (
	vertexLocationUS = "us"
	vertexLocationEU = "eu"

	// vertexMultiRegionPriceMultiplier 是 vertexModelLocations 内的模型走多区域端点时相对 global 价的倍数。
	vertexMultiRegionPriceMultiplier = 1.1
)

// isVertexMultiRegion 判断区域是否为 Vertex 多区域（us/eu）。
func isVertexMultiRegion(location string) bool {
	return location == vertexLocationUS || location == vertexLocationEU
}

// vertexAPIHost 返回区域对应的 Vertex AI API 主机：global 用全局主机，多区域用 aiplatform.{loc}.rep 主机，
// 单区域用 {loc}-aiplatform 主机。
func vertexAPIHost(location string) string {
	switch {
	case location == vertexGlobalLocation:
		return vertexGlobalHost
	case isVertexMultiRegion(location):
		return "aiplatform." + location + ".rep.googleapis.com"
	default:
		return location + "-aiplatform.googleapis.com"
	}
}

// vertexModelSupportsMultiRegion 判断模型是否在 vertexModelLocations 中声明支持该多区域。
func vertexModelSupportsMultiRegion(model, location string) bool {
	if !isVertexMultiRegion(location) {
		return false
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return vertexModelLocations[strings.TrimSpace(model)].multiRegion
}

// resolveVertexModelLocation 返回模型在账号区域下实际使用的区域：账号为多区域而模型不支持该多区域时用 global。
func resolveVertexModelLocation(accountLocation, model string) string {
	if isVertexMultiRegion(accountLocation) && !vertexModelSupportsMultiRegion(model, accountLocation) {
		return vertexGlobalLocation
	}
	return accountLocation
}

// vertexGeminiRequestLocation 返回 Vertex Gemini 请求实际发往的区域：未配置 project_id 的 API Key 账号
// 走 Express mode 全局端点；引用显式缓存时为缓存所在区域；其余按 VertexLocation(model)。
func vertexGeminiRequestLocation(account *Account, model, cachedContentLocation string) string {
	if account.IsVertexAPIKey() && account.VertexProjectID() == "" {
		return vertexGlobalLocation
	}
	if cachedContentLocation != "" {
		return cachedContentLocation
	}
	return account.VertexLocation(model)
}

// VertexUpstreamPriceMultiplier 返回 Vertex Gemini 账号请求 model 时相对 global 价的倍数：请求发往多区域端点
// 且模型在 vertexModelLocations 中声明支持该多区域时为 vertexMultiRegionPriceMultiplier，其余为 1。
// cachedContentLocation 为请求引用的显式缓存所在区域，未引用时为空。
func VertexUpstreamPriceMultiplier(account *Account, model, cachedContentLocation string) float64 {
	if account == nil || account.Platform != PlatformGemini || !account.IsVertexServiceAccount() {
		return 1
	}
	if vertexModelSupportsMultiRegion(model, vertexGeminiRequestLocation(account, model, cachedContentLocation)) {
		return vertexMultiRegionPriceMultiplier
	}
	return 1
}

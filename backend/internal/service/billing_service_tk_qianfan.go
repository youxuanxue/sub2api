package service

// tkDeepSeekPeakValleyExcludedModels are registry owners priced from Baidu
// Qianfan's published list rather than DeepSeek's direct API. Qianfan bills one
// flat rate with no time-of-day component, so the DeepSeek peak-valley policy in
// _config.deepseek_peak_valley must not double their output price during the
// 09:00-12:00 / 14:00-18:00 Asia/Shanghai windows. Without this list they would
// be caught by that policy's model_contains ["deepseek"] matcher and overbilled
// 2x for half the working day.
//
// Membership rule: an id belongs here only when its sole registry owner IS a
// Qianfan price. An id that bills from a DeepSeek official owner does NOT belong
// here, even when Qianfan also serves it — that includes dated aliases such as
// deepseek-v4-flash-0731, which resolves to the deepseek-v4-flash owner and is
// therefore subject to the official peak windows.
//
// These three ids have no DeepSeek official equivalent (Qianfan-exclusive SKUs),
// so the registry is their only legitimate global owner and this exclusion is
// what keeps that owner's flat rate flat.
var tkDeepSeekPeakValleyExcludedModels = map[string]struct{}{
	"deepseek-v3.2":       {},
	"deepseek-v3.2-think": {},
	"deepseek-ocr":        {},
}

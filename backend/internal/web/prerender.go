package web

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// crawlerUserAgents contains substrings to match known crawler/bot User-Agents.
var crawlerUserAgents = []string{
	"Googlebot",
	"Bingbot",
	"baiduspider",
	"Twitterbot",
	"TelegramBot",
	"facebookexternalhit",
	"WhatsApp",
	"LinkedInBot",
	"Slackbot",
	"Discordbot",
}

// isCrawler checks if the given User-Agent string belongs to a known crawler.
func isCrawler(ua string) bool {
	for _, bot := range crawlerUserAgents {
		if strings.Contains(ua, bot) {
			return true
		}
	}
	return false
}

// PrerenderMiddleware returns a Gin middleware that serves pre-rendered HTML
// to known search engine and social media crawlers. Normal users fall through
// to the SPA (existing behavior).
//
// This enables SEO and rich link previews for the Vue SPA without SSR/Nuxt.
func PrerenderMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ua := c.GetHeader("User-Agent")
		if !isCrawler(ua) {
			c.Next()
			return
		}

		path := c.Request.URL.Path

		var html string
		switch path {
		case "/", "/home":
			html = prerenderHome
		case "/pricing":
			html = prerenderPricing
		case "/quickstart":
			html = prerenderQuickstart
		default:
			// For routes we don't have prerender content for, fall through to SPA
			c.Next()
			return
		}

		c.Header("X-Prerender", "1")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
		c.Abort()
	}
}

const prerenderHome = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>TokenKey - AI API Gateway</title>
<meta name="description" content="TokenKey - AI API Gateway. 每一次调用，都是官方品质。文本、图像、视频，一个 Key 全搞定。Official quality AI API access.">
<meta property="og:type" content="website">
<meta property="og:title" content="TokenKey - AI API Gateway">
<meta property="og:description" content="每一次调用，都是官方品质。一个 API Key，所有主流 AI 模型。文本、图像、视频。订阅配额，费用可预测。">
<meta property="og:image" content="https://api.tokenkey.dev/og-cover.png">
<meta property="og:url" content="https://api.tokenkey.dev">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="TokenKey - AI API Gateway">
<meta name="twitter:description" content="Official quality AI API access. Text, image, video. One Key for everything.">
<meta name="twitter:image" content="https://api.tokenkey.dev/og-cover.png">
<link rel="canonical" href="https://api.tokenkey.dev/">
</head>
<body>
<h1>TokenKey - AI API Gateway</h1>
<p>每一次调用，都是官方品质。一个 API Key，所有主流 AI 模型。</p>

<section>
<h2>Official Quality</h2>
<p>官方品质 API 调用。直连官方接口，无中间商，延迟低、稳定性高。每一次请求都等同于直接调用官方 API。</p>
</section>

<section>
<h2>One Key for Everything</h2>
<p>一个 Key 全搞定。文本生成、图像创作、视频制作，所有主流 AI 模型统一接入，无需管理多个 API Key。</p>
</section>

<section>
<h2>Predictable Pricing</h2>
<p>订阅配额，费用可预测。按日/周/月订阅配额，团队共享，超限自动停。费用透明、可预期。</p>
</section>

<section>
<h2>Built-in Studio</h2>
<p>内置 Studio 创作工作台——Chat、Image、Video、BakeOff 多模型对比，一站式体验所有 AI 能力。</p>
</section>

<section>
<h2>Supported Providers</h2>
<ul>
<li>OpenAI (GPT-4o, GPT-4, o1, o3)</li>
<li>Anthropic (Claude Opus, Sonnet, Haiku)</li>
<li>Google (Gemini 2.5 Pro, Flash)</li>
<li>Amazon (Kiro)</li>
<li>xAI (Grok)</li>
<li>DeepSeek</li>
<li>Midjourney</li>
<li>Runway (Video)</li>
<li>Suno (Music)</li>
</ul>
</section>

<section>
<h2>Free Trial</h2>
<p>免费试用，送 100 万 tokens。足够测试你的真实工作流。只需邮箱，无需信用卡。</p>
<p>Start free with 1M tokens included. Enough to test your real workflow. Email only, no card required.</p>
</section>

<nav>
<a href="/pricing">View Pricing / 查看定价</a>
</nav>

<noscript>
<p>TokenKey requires JavaScript for the full interactive experience. Please enable JavaScript to access the dashboard, model catalog, and API management features.</p>
</noscript>
</body>
</html>`

const prerenderPricing = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>TokenKey 定价 - AI API Pricing</title>
<meta name="description" content="TokenKey AI API 定价方案。官方 API 定价，透明可预期。文本、图像、视频模型统一定价目录，实时模型可用性监控。">
<meta property="og:type" content="website">
<meta property="og:title" content="TokenKey 定价 - AI API Pricing">
<meta property="og:description" content="官方 API 定价，透明可预期。文本、图像、视频模型统一定价目录，实时可用性监控。订阅配额制费用可预测。">
<meta property="og:image" content="https://api.tokenkey.dev/og-cover.png">
<meta property="og:url" content="https://api.tokenkey.dev/pricing">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="TokenKey 定价 - AI API Pricing">
<meta name="twitter:description" content="Subscription-based AI API pricing. Predictable costs for text, image, and video models.">
<meta name="twitter:image" content="https://api.tokenkey.dev/og-cover.png">
<link rel="canonical" href="https://api.tokenkey.dev/pricing">
</head>
<body>
<h1>TokenKey 定价 - AI API Pricing</h1>
<p>官方 API 定价，透明可预期。文本、图像、视频模型统一定价目录，实时模型可用性监控。</p>

<section>
<h2>Pricing Model</h2>
<p>TokenKey offers transparent, official API pricing with real-time model availability monitoring. Subscription quota plans available for predictable costs.</p>
<ul>
<li>Official API pricing, per-model transparent rates</li>
<li>All mainstream AI models: text, image, and video</li>
<li>Real-time model availability monitoring</li>
<li>Subscription quota plans for predictable budgeting</li>
</ul>
</section>

<section>
<h2>Full Model Catalog</h2>
<p>JavaScript is required to view the full interactive pricing catalog with real-time model availability and detailed per-model quota information.</p>
</section>

<nav>
<a href="/">Back to Home / 返回首页</a>
</nav>

<noscript>
<p>TokenKey requires JavaScript to display the full interactive pricing catalog. Please enable JavaScript to see detailed model pricing, quota information, and subscription management.</p>
</noscript>
</body>
</html>`

const prerenderQuickstart = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Quick Start - TokenKey AI API Gateway</title>
<meta name="description" content="2 分钟开始使用 TokenKey AI API。获取 API Key，配置 Claude Code / Cursor / Codex / Cline，立即调用所有主流 AI 模型。">
<meta property="og:type" content="website">
<meta property="og:title" content="Quick Start - TokenKey AI API Gateway">
<meta property="og:description" content="Get started with TokenKey in 2 minutes. One API Key for Claude, GPT, Gemini, DeepSeek and more.">
<meta property="og:image" content="https://api.tokenkey.dev/og-cover.png">
<meta property="og:url" content="https://api.tokenkey.dev/quickstart">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="Quick Start - TokenKey AI API Gateway">
<meta name="twitter:description" content="2 minutes to AI API access. One Key for Claude, GPT, Gemini, DeepSeek.">
<meta name="twitter:image" content="https://api.tokenkey.dev/og-cover.png">
<link rel="canonical" href="https://api.tokenkey.dev/quickstart">
</head>
<body>
<h1>Quick Start - TokenKey AI API Gateway</h1>
<p>2 分钟开始使用 TokenKey。获取 API Key，配置你的开发工具，立即调用所有主流 AI 模型。</p>

<section>
<h2>Supported Tools</h2>
<ul>
<li>Claude Code</li>
<li>Cursor</li>
<li>OpenAI Codex CLI</li>
<li>Cline (VS Code)</li>
<li>Python / Node.js OpenAI SDK</li>
<li>Any OpenAI-compatible client</li>
</ul>
</section>

<section>
<h2>Free Trial</h2>
<p>Start free with 1M tokens included. Enough to test your real workflow. Email only, no card required.</p>
</section>

<nav>
<a href="/">Home</a>
<a href="/pricing">Pricing</a>
</nav>

<noscript>
<p>TokenKey requires JavaScript for the interactive quick start guide. Please enable JavaScript to get your API key and configuration snippets.</p>
</noscript>
</body>
</html>`

package web

import (
	"fmt"
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
			html = prerenderHomeHTML()
		case "/pricing":
			html = prerenderPricingHTML()
		case "/quickstart":
			html = prerenderQuickstartHTML()
		default:
			c.Next()
			return
		}

		c.Header("X-Prerender", "1")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
		c.Abort()
	}
}

func prerenderHead(title, description, ogDescription, canonicalPath string) string {
	canonicalURL := storefrontCanonicalOrigin + canonicalPath
	return fmt.Sprintf(`<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<meta name="description" content="%s">
<meta property="og:type" content="website">
<meta property="og:title" content="%s">
<meta property="og:description" content="%s">
<meta property="og:image" content="%s">
<meta property="og:url" content="%s">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="%s">
<meta name="twitter:description" content="%s">
<meta name="twitter:image" content="%s">
<link rel="canonical" href="%s">`,
		title, description, title, ogDescription, storefrontOGImageURL,
		canonicalURL, title, storefrontENTwitterDescription, storefrontOGImageURL, canonicalURL)
}

func prerenderHomeHTML() string {
	head := prerenderHead(storefrontSiteTitle, storefrontZHMetaDescription, storefrontZHOGDescription, "/")
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
%s
</head>
<body>
<h1>%s</h1>
<p>%s</p>

<section>
<h2>Direct to Official APIs</h2>
<p>官方品质 API 调用。直连官方接口，无中间商，延迟低、稳定性高。每一次请求都等同于直接调用官方 API。</p>
</section>

<section>
<h2>Every Modality, One Key</h2>
<p>一个 Key 全搞定。文本生成、图像创作、视频制作，所有主流 AI 模型统一接入，无需管理多个 API Key。</p>
</section>

<section>
<h2>Predictable, Quota-based Pricing</h2>
<p>订阅配额，费用可预测。按日/周/月订阅配额，团队共享，超限自动停。费用透明、可预期。</p>
</section>

<section>
<h2>Built-in Studio Workspace</h2>
<p>内置 Studio 创作工作台——Chat、Image、Video、BakeOff 多模型对比，一站式体验所有 AI 能力。</p>
</section>

<section>
<h2>Supported Models</h2>
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
<h2>Get Started Free</h2>
<p>%s</p>
<p>%s</p>
</section>

<nav>
<a href="/pricing">View Pricing / 查看定价</a>
</nav>

<noscript>
<p>Enable JavaScript to access the dashboard, model catalog, and API management features.</p>
</noscript>
</body>
</html>`, head, storefrontSiteTitle, storefrontZHHeroSubtitle, storefrontZHFreeTrial, storefrontENFreeTrial)
}

func prerenderPricingHTML() string {
	title := "TokenKey 定价 - AI API Pricing"
	desc := "TokenKey AI API 定价方案。官方 API 定价，透明可预期。文本、图像、视频模型统一定价目录，实时模型可用性监控。"
	ogDesc := "官方 API 定价，透明可预期。文本、图像、视频模型统一定价目录，实时可用性监控。订阅配额制费用可预测。"
	head := prerenderHead(title, desc, ogDesc, "/pricing")
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
%s
</head>
<body>
<h1>%s</h1>
<p>%s</p>

<section>
<h2>Pricing</h2>
<p>Transparent, per-model pricing with no markup on vendor rates. Quota-based plans let you cap spend before it happens.</p>
<ul>
<li>Vendor-rate pricing with per-model transparency</li>
<li>Text, image, and video models in one catalog</li>
<li>Live model availability dashboard</li>
<li>Quota plans for predictable monthly budgets</li>
</ul>
</section>

<section>
<h2>Full Model Catalog</h2>
<p>Enable JavaScript to browse the interactive pricing catalog with live availability and per-model quota details.</p>
</section>

<nav>
<a href="/">Back to Home / 返回首页</a>
</nav>

<noscript>
<p>Enable JavaScript to view the full interactive pricing catalog, quota details, and subscription management.</p>
</noscript>
</body>
</html>`, head, title, desc)
}

func prerenderQuickstartHTML() string {
	title := "Quick Start - TokenKey AI API Gateway"
	desc := "2 分钟开始使用 TokenKey AI API。获取 API Key，配置 Claude Code / Cursor / Codex / Cline，立即调用所有主流 AI 模型。"
	ogDesc := "Get started in 2 minutes. One API key for Claude, GPT, Gemini, DeepSeek, and more."
	head := prerenderHead(title, desc, ogDesc, "/quickstart")
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
%s
</head>
<body>
<h1>%s</h1>
<p>%s</p>

<section>
<h2>Works With</h2>
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
<h2>Get Started Free</h2>
<p>%s</p>
</section>

<nav>
<a href="/">Home</a>
<a href="/pricing">Pricing</a>
</nav>

<noscript>
<p>Enable JavaScript for the interactive quick-start guide, API key generation, and configuration snippets.</p>
</noscript>
</body>
</html>`, head, title, desc, storefrontENFreeTrial)
}

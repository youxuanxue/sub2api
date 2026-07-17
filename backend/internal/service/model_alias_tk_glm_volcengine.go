package service

import (
	"regexp"
	"strings"
)

// VolcEngine Ark exposes GLM chat SKUs as glm-{major}-{minor}-{YYMMDD|YYYYMMDD}
// (e.g. glm-4-7-251222). TokenKey serves GLM via Qwen/DashScope as glm-4.7.
// Clients that still send the VolcEngine dated id should route/bill as glm-4.7,
// not fail after the VolcEngine account mapping was withdrawn.
var tkGLMVolcengineDatedModelPattern = regexp.MustCompile(`^glm-(\d+)-(\d+)-(\d{6}|\d{8})$`)

func normalizeGLMVolcengineDatedModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	sub := tkGLMVolcengineDatedModelPattern.FindStringSubmatch(m)
	if sub == nil {
		return ""
	}
	return "glm-" + sub[1] + "." + sub[2]
}

// gateway-capability-catalog exports the compiled public catalog without starting
// the gateway, opening a database, or modifying runtime capability policy.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type model struct {
	ID           string   `json:"id"`
	Vendor       string   `json:"vendor"`
	Mode         string   `json:"mode"`
	Capabilities []string `json:"capabilities"`
}

func main() {
	cfg := &config.Config{}
	cfg.Pricing.FallbackFile = "resources/model-pricing/model_prices_and_context_window.json"
	catalog := service.NewPricingCatalogService(cfg).BuildPublicCatalog(context.Background())
	if len(catalog.Data) == 0 {
		fmt.Fprintln(os.Stderr, "empty compiled catalog; run from backend/")
		os.Exit(1)
	}
	models := make([]model, 0, len(catalog.Data))
	for _, entry := range catalog.Data {
		mode := entry.Pricing.BillingMode
		if mode == "" || mode == "token" {
			mode = "chat"
		}
		caps := append([]string{}, entry.Capabilities...)
		sort.Strings(caps)
		models = append(models, model{entry.ModelID, entry.Vendor, mode, caps})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Schema    int                       `json:"schema"`
		Models    []model                   `json:"models"`
		Protocols []protocolrouter.Protocol `json:"protocols"`
	}{1, models, protocolrouter.AllProtocols()}); err != nil {
		os.Exit(1)
	}
}

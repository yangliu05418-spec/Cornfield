package main

import (
	"fmt"
	"strings"

	"internal-image-studio/internal/modelconfig"
)

// Preserve the full catalog hash for the deployed-revision check.
func filterCanaryModels(catalog *modelconfig.Catalog, selection string) error {
	models := []modelconfig.Model{}
	seen := map[string]bool{}
	for _, value := range strings.Split(selection, ",") {
		id := strings.TrimSpace(value)
		model, ok := catalog.Find(id)
		if !ok || !model.Enabled {
			return fmt.Errorf("canary model %q is unknown or disabled", id)
		}
		if !seen[id] {
			models = append(models, model)
			seen[id] = true
		}
	}
	catalog.Models = models
	return nil
}

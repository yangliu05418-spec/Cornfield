package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/config"
	"internal-image-studio/internal/modelconfig"
)

func main() {
	flag.Parse()
	command := "validate"
	if flag.NArg() > 0 {
		command = flag.Arg(0)
	}
	path := os.Getenv("MODEL_CONFIG_PATH")
	if path == "" {
		path = "../config/models.yaml"
	}
	catalog, err := modelconfig.Load(path)
	if err != nil {
		log.Fatal(err)
	}
	if command == "validate" {
		fmt.Printf("valid: %d models, capability revision %s\n", len(catalog.Models), catalog.Hash)
		return
	}
	if command == "verify-remote" {
		verifyRemote(catalog)
		return
	}
	if command != "apply" {
		log.Fatalf("unknown command %q (use validate, verify-remote, or apply)", command)
	}
	ctx := context.Background()
	databaseURL, err := config.DatabaseURLFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	modelIDs, err := currentModelIDs(catalog.Models)
	if err != nil {
		log.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, model := range catalog.Models {
		configJSON, _ := json.Marshal(model)
		_, err = tx.Exec(ctx, `INSERT INTO models(id,provider_id,provider_model,display_name,enabled,sort_order,current_revision)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET provider_id=excluded.provider_id,provider_model=excluded.provider_model,
			display_name=excluded.display_name,enabled=excluded.enabled,sort_order=excluded.sort_order,current_revision=excluded.current_revision,updated_at=now()`,
			model.ID, model.Provider, model.ProviderModel, model.DisplayName, model.Enabled, model.Order, catalog.Hash)
		if err != nil {
			log.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO model_capability_versions(model_id,revision,config) VALUES($1,$2,$3)
			ON CONFLICT (model_id,revision) DO NOTHING`, model.ID, catalog.Hash, configJSON)
		if err != nil {
			log.Fatal(err)
		}
		var storedConfig []byte
		if err = tx.QueryRow(ctx, `SELECT config FROM model_capability_versions WHERE model_id=$1 AND revision=$2`, model.ID, catalog.Hash).Scan(&storedConfig); err != nil {
			log.Fatal(err)
		}
		if !capabilitySnapshotMatches(storedConfig, configJSON) {
			log.Fatalf("immutable capability snapshot conflict for model %s revision %s", model.ID, catalog.Hash)
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE models SET enabled=false,updated_at=now()
		WHERE enabled=true AND NOT (id=ANY($1::text[]))`, modelIDs); err != nil {
		log.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("applied capability revision %s\n", catalog.Hash)
}

func capabilitySnapshotMatches(stored, expected []byte) bool {
	normalizedStored, storedErr := modelconfig.NormalizeSnapshotJSON(stored)
	normalizedExpected, expectedErr := modelconfig.NormalizeSnapshotJSON(expected)
	return storedErr == nil && expectedErr == nil && bytes.Equal(normalizedStored, normalizedExpected)
}

func currentModelIDs(models []modelconfig.Model) ([]string, error) {
	if len(models) == 0 {
		return nil, fmt.Errorf("refusing to disable models without a non-empty current catalog")
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" {
			return nil, fmt.Errorf("current catalog contains a model without an id")
		}
		ids = append(ids, model.ID)
	}
	return ids, nil
}

func verifyRemote(catalog *modelconfig.Catalog) {
	baseURL := os.Getenv("OPENROUTER_BASE_URL")
	apiKey, err := secret("OPENROUTER_API_KEY")
	if err != nil {
		log.Fatal(err)
	}
	report, err := modelconfig.VerifyOpenRouterRemote(context.Background(), catalog, nil, baseURL, apiKey)
	if err != nil {
		log.Fatal(err)
	}
	for _, model := range report.Models {
		if len(model.Drifts) == 0 {
			fmt.Printf("verified %s: %d endpoints\n", model.ModelID, model.CheckedEndpoints)
			continue
		}
		for _, drift := range model.Drifts {
			fmt.Printf("drift %s: %s\n", model.ModelID, drift)
		}
	}
	for _, model := range catalog.Models {
		if model.Provider == "legnext" {
			fmt.Printf("skipped %s: Legnext has no machine-readable capability endpoint\n", model.ID)
		}
	}
	if report.HasDrift() {
		log.Fatal("remote capability drift detected")
	}
}

func secret(name string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value, nil
	}
	path := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	return strings.TrimSpace(string(data)), nil
}

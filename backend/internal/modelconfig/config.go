package modelconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	Revision int     `yaml:"revision" json:"revision"`
	Models   []Model `yaml:"models" json:"models"`
	Hash     string  `yaml:"-" json:"hash"`
}

type Model struct {
	ID                string       `yaml:"id" json:"id"`
	DisplayName       string       `yaml:"display_name" json:"display_name"`
	Provider          string       `yaml:"provider" json:"provider"`
	ProviderModel     string       `yaml:"provider_model" json:"provider_model"`
	Enabled           bool         `yaml:"enabled" json:"enabled"`
	Order             int          `yaml:"order" json:"order"`
	PromptSuffix      string       `yaml:"prompt_suffix,omitempty" json:"prompt_suffix,omitempty"`
	PromptAspectRatio bool         `yaml:"prompt_aspect_ratio,omitempty" json:"prompt_aspect_ratio,omitempty"`
	RequestParameters []string     `yaml:"request_parameters,omitempty" json:"request_parameters,omitempty"`
	OutputsPerDraw    int          `yaml:"outputs_per_draw" json:"outputs_per_draw"`
	Capabilities      Capabilities `yaml:"capabilities" json:"capabilities"`
	Policy            Policy       `yaml:"policy" json:"policy"`
}

type Capabilities struct {
	TextToImage        bool      `yaml:"text_to_image" json:"text_to_image"`
	ImageToImage       bool      `yaml:"image_to_image" json:"image_to_image"`
	AspectRatios       []string  `yaml:"aspect_ratios" json:"aspect_ratios"`
	Resolutions        []string  `yaml:"resolutions" json:"resolutions"`
	Qualities          []string  `yaml:"qualities,omitempty" json:"qualities,omitempty"`
	MaxReferenceImages int       `yaml:"max_reference_images" json:"max_reference_images"`
	MaxReferenceBytes  int64     `yaml:"max_reference_bytes" json:"max_reference_bytes"`
	DrawCount          DrawCount `yaml:"draw_count" json:"draw_count"`
	MidjourneyVersions []string  `yaml:"midjourney_versions,omitempty" json:"midjourney_versions,omitempty"`
}

type DrawCount struct {
	Min     int `yaml:"min" json:"min"`
	Max     int `yaml:"max" json:"max"`
	Default int `yaml:"default" json:"default"`
}

type Policy struct {
	SubmitTimeoutSeconds     int      `yaml:"submit_timeout_seconds" json:"submit_timeout_seconds"`
	GenerationTimeoutSeconds int      `yaml:"generation_timeout_seconds" json:"generation_timeout_seconds"`
	MaxConcurrency           int      `yaml:"max_concurrency" json:"max_concurrency"`
	MaxSafeRetries           int      `yaml:"max_safe_retries" json:"max_safe_retries"`
	BreakerMinRequests       int      `yaml:"breaker_min_requests" json:"breaker_min_requests"`
	BreakerFailureRatio      float64  `yaml:"breaker_failure_ratio" json:"breaker_failure_ratio"`
	BreakerCooldownSeconds   int      `yaml:"breaker_cooldown_seconds" json:"breaker_cooldown_seconds"`
	AllowedOutputHosts       []string `yaml:"allowed_output_hosts" json:"allowed_output_hosts"`
}

var legacyPolicyJSONKeys = [][2]string{
	{"SubmitTimeoutSeconds", "submit_timeout_seconds"},
	{"GenerationTimeoutSeconds", "generation_timeout_seconds"},
	{"MaxConcurrency", "max_concurrency"},
	{"MaxSafeRetries", "max_safe_retries"},
	{"BreakerMinRequests", "breaker_min_requests"},
	{"BreakerFailureRatio", "breaker_failure_ratio"},
	{"BreakerCooldownSeconds", "breaker_cooldown_seconds"},
	{"AllowedOutputHosts", "allowed_output_hosts"},
}

func (p *Policy) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if err := normalizeLegacyPolicyJSON(fields); err != nil {
		return err
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	type plainPolicy Policy
	return json.Unmarshal(normalized, (*plainPolicy)(p))
}

// NormalizeSnapshotJSON canonicalizes the one historical Policy key format
// without weakening immutable snapshot comparison for any other field.
func NormalizeSnapshotJSON(data []byte) ([]byte, error) {
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	policyData, ok := snapshot["policy"]
	if ok {
		var policy map[string]json.RawMessage
		if err := json.Unmarshal(policyData, &policy); err != nil {
			return nil, err
		}
		if err := normalizeLegacyPolicyJSON(policy); err != nil {
			return nil, err
		}
		normalizedPolicy, err := json.Marshal(policy)
		if err != nil {
			return nil, err
		}
		snapshot["policy"] = normalizedPolicy
	}
	normalizedSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return canonicalizeSnapshotJSON(normalizedSnapshot)
}

func canonicalizeSnapshotJSON(data []byte) ([]byte, error) {
	snapshot, err := decodeSnapshotJSON(data)
	if err != nil {
		return nil, err
	}
	// PostgreSQL jsonb does not preserve object-key order at any nesting
	// level. Re-encoding a generic JSON value recursively sorts every object
	// while retaining arrays, unknown fields, and numeric tokens.
	return json.Marshal(snapshot)
}

// SnapshotJSONEqual compares immutable capability snapshots with PostgreSQL
// jsonb semantics: object key order and numeric notation are insignificant,
// while types, unknown fields, and array order remain significant.
func SnapshotJSONEqual(left, right []byte) (bool, error) {
	normalizedLeft, err := NormalizeSnapshotJSON(left)
	if err != nil {
		return false, err
	}
	normalizedRight, err := NormalizeSnapshotJSON(right)
	if err != nil {
		return false, err
	}
	leftValue, err := decodeSnapshotJSON(normalizedLeft)
	if err != nil {
		return false, err
	}
	rightValue, err := decodeSnapshotJSON(normalizedRight)
	if err != nil {
		return false, err
	}
	return snapshotValuesEqual(leftValue, rightValue), nil
}

func decodeSnapshotJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var snapshot any
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func snapshotValuesEqual(left, right any) bool {
	switch leftValue := left.(type) {
	case nil:
		return right == nil
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftNumber, leftOK := new(big.Rat).SetString(leftValue.String())
		rightNumber, rightOK := new(big.Rat).SetString(rightValue.String())
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	case []any:
		rightValue, ok := right.([]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !snapshotValuesEqual(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		rightValue, ok := right.(map[string]any)
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, leftField := range leftValue {
			rightField, exists := rightValue[key]
			if !exists || !snapshotValuesEqual(leftField, rightField) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func normalizeLegacyPolicyJSON(fields map[string]json.RawMessage) error {
	for _, names := range legacyPolicyJSONKeys {
		legacyName, currentName := names[0], names[1]
		legacyValue, legacyExists := fields[legacyName]
		if !legacyExists {
			continue
		}
		if _, currentExists := fields[currentName]; currentExists {
			return fmt.Errorf("policy contains both %s and %s", legacyName, currentName)
		}
		fields[currentName] = legacyValue
		delete(fields, legacyName)
	}
	return nil
}

func Load(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model catalog: %w", err)
	}
	var catalog Catalog
	if err := yaml.Unmarshal(b, &catalog); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	canonical, err := yaml.Marshal(struct {
		Revision int     `yaml:"revision"`
		Models   []Model `yaml:"models"`
	}{Revision: catalog.Revision, Models: catalog.Models})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	catalog.Hash = hex.EncodeToString(sum[:])
	sort.SliceStable(catalog.Models, func(i, j int) bool { return catalog.Models[i].Order < catalog.Models[j].Order })
	return &catalog, nil
}

func (c Catalog) Validate() error {
	if c.Revision < 1 || len(c.Models) == 0 {
		return errors.New("model catalog requires a positive revision and at least one model")
	}
	seen := make(map[string]struct{}, len(c.Models))
	for _, m := range c.Models {
		if m.ID == "" || m.DisplayName == "" || m.Provider == "" || m.ProviderModel == "" {
			return fmt.Errorf("model has missing identity fields: %q", m.ID)
		}
		if _, ok := seen[m.ID]; ok {
			return fmt.Errorf("duplicate model id %q", m.ID)
		}
		seen[m.ID] = struct{}{}
		if m.OutputsPerDraw < 1 || m.OutputsPerDraw > 16 {
			return fmt.Errorf("model %s has invalid outputs_per_draw", m.ID)
		}
		d := m.Capabilities.DrawCount
		if d.Min < 1 || d.Max > 4 || d.Default < d.Min || d.Default > d.Max {
			return fmt.Errorf("model %s has invalid draw count capability", m.ID)
		}
		if d.Max*m.OutputsPerDraw > 16 {
			return fmt.Errorf("model %s can exceed the 16-output batch limit", m.ID)
		}
		if err := validateCapabilities(m); err != nil {
			return err
		}
		if err := validatePolicy(m); err != nil {
			return err
		}
	}
	if _, err := c.ProviderConcurrency(); err != nil {
		return err
	}
	return nil
}

// ProviderConcurrency returns the provider-wide limits declared by enabled
// models. A provider has one upstream capacity, so every enabled model routed
// through it must agree on the same value.
func (c Catalog) ProviderConcurrency() (map[string]int, error) {
	limits := make(map[string]int)
	for _, model := range c.Models {
		if !model.Enabled {
			continue
		}
		limit := model.Policy.MaxConcurrency
		if existing, ok := limits[model.Provider]; ok && existing != limit {
			return nil, fmt.Errorf("provider %s has inconsistent max_concurrency values %d and %d", model.Provider, existing, limit)
		}
		limits[model.Provider] = limit
	}
	return limits, nil
}

func validateCapabilities(m Model) error {
	capabilities := m.Capabilities
	if !capabilities.TextToImage && !capabilities.ImageToImage {
		return fmt.Errorf("model %s must support at least one generation mode", m.ID)
	}
	seenRatios := make(map[string]struct{}, len(capabilities.AspectRatios))
	for _, ratio := range capabilities.AspectRatios {
		parts := strings.Split(ratio, ":")
		if len(parts) != 2 {
			return fmt.Errorf("model %s has invalid aspect ratio %q", m.ID, ratio)
		}
		width, widthErr := strconv.Atoi(parts[0])
		height, heightErr := strconv.Atoi(parts[1])
		if widthErr != nil || heightErr != nil || width < 1 || height < 1 {
			return fmt.Errorf("model %s has invalid aspect ratio %q", m.ID, ratio)
		}
		if _, exists := seenRatios[ratio]; exists {
			return fmt.Errorf("model %s has duplicate aspect ratio %q", m.ID, ratio)
		}
		seenRatios[ratio] = struct{}{}
	}
	if duplicateOrBlank(capabilities.Resolutions) {
		return fmt.Errorf("model %s has blank or duplicate resolutions", m.ID)
	}
	if duplicateOrBlank(capabilities.Qualities) {
		return fmt.Errorf("model %s has blank or duplicate qualities", m.ID)
	}
	if capabilities.MaxReferenceImages < 0 || capabilities.MaxReferenceImages > 16 {
		return fmt.Errorf("model %s has invalid max_reference_images", m.ID)
	}
	const uploadByteLimit = 25 << 20
	if capabilities.ImageToImage && (capabilities.MaxReferenceImages == 0 || capabilities.MaxReferenceBytes < 1 || capabilities.MaxReferenceBytes > uploadByteLimit) {
		return fmt.Errorf("model %s enables image_to_image without valid reference image capacity", m.ID)
	}
	if !capabilities.ImageToImage && (capabilities.MaxReferenceImages != 0 || capabilities.MaxReferenceBytes != 0) {
		return fmt.Errorf("model %s has reference image capacity while image_to_image is disabled", m.ID)
	}
	if duplicateOrBlank(m.RequestParameters) {
		return fmt.Errorf("model %s has blank or duplicate request_parameters", m.ID)
	}

	parameters := make(map[string]struct{}, len(m.RequestParameters))
	for _, parameter := range m.RequestParameters {
		parameters[parameter] = struct{}{}
	}
	has := func(parameter string) bool {
		_, ok := parameters[parameter]
		return ok
	}
	switch m.Provider {
	case "openrouter":
		if capabilities.ImageToImage && !has("input_references") {
			return fmt.Errorf("model %s enables image_to_image without OpenRouter input_references", m.ID)
		}
		if len(capabilities.AspectRatios) > 0 && !has("aspect_ratio") && !m.PromptAspectRatio {
			return fmt.Errorf("model %s advertises selectable aspect ratios without OpenRouter aspect_ratio", m.ID)
		}
		if len(capabilities.Resolutions) > 0 && !has("resolution") {
			return fmt.Errorf("model %s advertises selectable resolutions without OpenRouter resolution", m.ID)
		}
		if m.OutputsPerDraw > 1 && !has("n") {
			return fmt.Errorf("model %s produces multiple outputs without OpenRouter n", m.ID)
		}
		if len(capabilities.Qualities) > 0 && !has("quality") {
			return fmt.Errorf("model %s advertises selectable qualities without OpenRouter quality", m.ID)
		}
	case "bfl":
		if len(capabilities.AspectRatios) == 0 || len(capabilities.Resolutions) == 0 {
			return fmt.Errorf("model %s requires BFL aspect ratios and resolution tiers", m.ID)
		}
		if capabilities.ImageToImage && capabilities.MaxReferenceImages > 8 {
			return fmt.Errorf("model %s exceeds BFL's eight reference image limit", m.ID)
		}
		if len(m.Policy.AllowedOutputHosts) == 0 {
			return fmt.Errorf("model %s requires an output host allowlist", m.ID)
		}
	case "legnext":
		if len(m.Policy.AllowedOutputHosts) == 0 {
			return fmt.Errorf("model %s requires an output host allowlist", m.ID)
		}
		if len(capabilities.MidjourneyVersions) > 0 && duplicateOrBlank(capabilities.MidjourneyVersions) {
			return fmt.Errorf("model %s has blank or duplicate Midjourney versions", m.ID)
		}
		supportedVersions := map[string]bool{"6": true, "6.1": true, "7": true, "8": true, "8.1": true, "niji 6": true}
		for _, version := range capabilities.MidjourneyVersions {
			if !supportedVersions[version] {
				return fmt.Errorf("model %s has unsupported Midjourney version %q", m.ID, version)
			}
		}
	default:
		return fmt.Errorf("model %s uses unsupported provider %q", m.ID, m.Provider)
	}
	return nil
}

func validatePolicy(m Model) error {
	policy := m.Policy
	if policy.SubmitTimeoutSeconds < 1 || policy.GenerationTimeoutSeconds < 1 || policy.MaxConcurrency < 1 || policy.MaxSafeRetries < 0 {
		return fmt.Errorf("model %s has invalid timeout, concurrency, or retry policy", m.ID)
	}
	if policy.BreakerMinRequests < 1 || policy.BreakerFailureRatio <= 0 || policy.BreakerFailureRatio > 1 || policy.BreakerCooldownSeconds < 1 {
		return fmt.Errorf("model %s has invalid breaker policy", m.ID)
	}
	if duplicateOrBlank(policy.AllowedOutputHosts) {
		return fmt.Errorf("model %s has blank or duplicate output hosts", m.ID)
	}
	for _, host := range policy.AllowedOutputHosts {
		if host == "delivery.*.bfl.ai" {
			continue
		}
		if host != strings.ToLower(host) || strings.ContainsAny(host, "/:*?#@") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
			return fmt.Errorf("model %s has invalid output host %q", m.ID, host)
		}
	}
	return nil
}

func duplicateOrBlank(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func (c Catalog) Find(id string) (Model, bool) {
	for _, model := range c.Models {
		if model.ID == id && model.Enabled {
			return model, true
		}
	}
	return Model{}, false
}

package modelconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type RemoteReport struct {
	Models []RemoteModelReport
}

type RemoteModelReport struct {
	ModelID          string
	ProviderModel    string
	CheckedEndpoints int
	Drifts           []string
}

func (r RemoteReport) HasDrift() bool {
	for _, model := range r.Models {
		if len(model.Drifts) > 0 {
			return true
		}
	}
	return false
}

// VerifyOpenRouterRemote compares the request parameters that Cornfield may
// send with every endpoint currently advertised for the same OpenRouter model.
func VerifyOpenRouterRemote(ctx context.Context, catalog *Catalog, client *http.Client, baseURL, apiKey string) (RemoteReport, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if baseURL == "" {
		baseURL = "https://openrouter.ai"
	}

	report := RemoteReport{}
	for _, model := range catalog.Models {
		if model.Provider != "openrouter" {
			continue
		}
		remote, err := fetchOpenRouterEndpoints(ctx, client, baseURL, apiKey, model.ProviderModel)
		if err != nil {
			return RemoteReport{}, fmt.Errorf("verify %s: %w", model.ID, err)
		}
		modelReport := RemoteModelReport{ModelID: model.ID, ProviderModel: model.ProviderModel, CheckedEndpoints: len(remote.Endpoints)}
		if remote.ID != "" && remote.ID != model.ProviderModel {
			modelReport.Drifts = append(modelReport.Drifts, fmt.Sprintf("remote model id is %q", remote.ID))
		}
		if len(remote.Endpoints) == 0 {
			modelReport.Drifts = append(modelReport.Drifts, "remote model has no endpoints")
		}
		for _, endpoint := range remote.Endpoints {
			name := endpoint.ProviderName
			if name == "" {
				name = "unnamed endpoint"
			}
			for _, parameter := range model.RequestParameters {
				if !openRouterEndpointSupports(parameter, endpoint.SupportedParameters) {
					modelReport.Drifts = append(modelReport.Drifts, fmt.Sprintf("%s no longer supports %s", name, parameter))
				}
			}
			compareRemoteRange(&modelReport, name, "n", model.OutputsPerDraw, endpoint.SupportedParameters)
			if model.Capabilities.ImageToImage {
				compareRemoteRange(&modelReport, name, "input_references", model.Capabilities.MaxReferenceImages, endpoint.SupportedParameters)
			}
			compareRemoteEnum(&modelReport, name, "aspect_ratio", model.Capabilities.AspectRatios, endpoint.SupportedParameters)
			compareRemoteEnum(&modelReport, name, "resolution", model.Capabilities.Resolutions, endpoint.SupportedParameters)
			compareRemoteEnum(&modelReport, name, "quality", model.Capabilities.Qualities, endpoint.SupportedParameters)
		}
		report.Models = append(report.Models, modelReport)
	}
	return report, nil
}

func openRouterEndpointSupports(parameter string, supported map[string]openRouterParameter) bool {
	if _, ok := supported[parameter]; ok {
		return true
	}
	// size is a normalized Image API shorthand. OpenRouter converts explicit
	// pixels for endpoints that expose the underlying resolution and aspect
	// ratio controls even when discovery does not repeat the alias.
	if parameter == "size" {
		_, hasResolution := supported["resolution"]
		_, hasAspectRatio := supported["aspect_ratio"]
		return hasResolution && hasAspectRatio
	}
	return false
}

type openRouterEndpoints struct {
	ID        string               `json:"id"`
	Endpoints []openRouterEndpoint `json:"endpoints"`
}

type openRouterEndpoint struct {
	ProviderName        string                         `json:"provider_name"`
	SupportedParameters map[string]openRouterParameter `json:"supported_parameters"`
}

type openRouterParameter struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
	Min    *int     `json:"min"`
	Max    *int     `json:"max"`
}

func fetchOpenRouterEndpoints(ctx context.Context, client *http.Client, baseURL, apiKey, model string) (openRouterEndpoints, error) {
	parts := strings.Split(model, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return openRouterEndpoints{}, fmt.Errorf("invalid provider model %q", model)
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/images/models/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/endpoints"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return openRouterEndpoints{}, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := client.Do(req)
	if err != nil {
		return openRouterEndpoints{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		return openRouterEndpoints{}, fmt.Errorf("remote returned HTTP %d", res.StatusCode)
	}
	var remote openRouterEndpoints
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&remote); err != nil {
		return openRouterEndpoints{}, fmt.Errorf("decode remote capabilities: %w", err)
	}
	return remote, nil
}

func compareRemoteEnum(report *RemoteModelReport, endpointName, parameter string, configured []string, remote map[string]openRouterParameter) {
	descriptor, ok := remote[parameter]
	if !ok || len(descriptor.Values) == 0 {
		return
	}
	available := make(map[string]struct{}, len(descriptor.Values))
	for _, value := range descriptor.Values {
		available[value] = struct{}{}
	}
	for _, value := range configured {
		if _, ok := available[value]; !ok {
			report.Drifts = append(report.Drifts, fmt.Sprintf("%s %s no longer allows %s", endpointName, parameter, value))
		}
	}
}

func compareRemoteRange(report *RemoteModelReport, endpointName, parameter string, configured int, remote map[string]openRouterParameter) {
	descriptor, ok := remote[parameter]
	if !ok || configured <= 0 {
		return
	}
	if descriptor.Min != nil && configured < *descriptor.Min {
		report.Drifts = append(report.Drifts, fmt.Sprintf("%s %s now requires at least %d", endpointName, parameter, *descriptor.Min))
	}
	if descriptor.Max != nil && configured > *descriptor.Max {
		report.Drifts = append(report.Drifts, fmt.Sprintf("%s %s now allows at most %d", endpointName, parameter, *descriptor.Max))
	}
}

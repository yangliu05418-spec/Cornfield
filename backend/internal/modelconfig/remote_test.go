package modelconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyOpenRouterRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/images/models/author/model/endpoints" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"author/model",
			"endpoints":[{
				"provider_name":"Provider A",
				"supported_parameters":{
					"n":{"type":"range","min":1,"max":10},
					"input_references":{"type":"range","min":0,"max":16}
				}
			}]
		}`))
	}))
	defer server.Close()

	catalog := &Catalog{Revision: 1, Models: []Model{validOpenRouterModel()}}
	report, err := VerifyOpenRouterRemote(context.Background(), catalog, server.Client(), server.URL, "test-key")
	if err != nil {
		t.Fatalf("VerifyOpenRouterRemote: %v", err)
	}
	if report.HasDrift() || len(report.Models) != 1 || report.Models[0].CheckedEndpoints != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyOpenRouterRemoteReportsShrunkenRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"author/model",
			"endpoints":[{
				"provider_name":"Provider A",
				"supported_parameters":{
					"n":{"type":"range","min":1,"max":10},
					"input_references":{"type":"range","min":0,"max":1}
				}
			}]
		}`))
	}))
	defer server.Close()

	catalog := &Catalog{Revision: 1, Models: []Model{validOpenRouterModel()}}
	report, err := VerifyOpenRouterRemote(context.Background(), catalog, server.Client(), server.URL, "")
	if err != nil {
		t.Fatalf("VerifyOpenRouterRemote: %v", err)
	}
	if !report.HasDrift() || len(report.Models[0].Drifts) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyOpenRouterRemoteReportsMissingParameter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"id":"author/model",
			"endpoints":[{
				"provider_name":"Provider A",
				"supported_parameters":{"n":{"type":"integer"}}
			}]
		}`))
	}))
	defer server.Close()

	catalog := &Catalog{Revision: 1, Models: []Model{validOpenRouterModel()}}
	report, err := VerifyOpenRouterRemote(context.Background(), catalog, server.Client(), server.URL, "")
	if err != nil {
		t.Fatalf("VerifyOpenRouterRemote: %v", err)
	}
	if !report.HasDrift() || len(report.Models[0].Drifts) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestOpenRouterSizeAliasRequiresResolutionAndAspectRatio(t *testing.T) {
	supported := map[string]openRouterParameter{"resolution": {}, "aspect_ratio": {}}
	if !openRouterEndpointSupports("size", supported) {
		t.Fatal("size alias was not accepted for a convertible endpoint")
	}
	delete(supported, "aspect_ratio")
	if openRouterEndpointSupports("size", supported) {
		t.Fatal("size alias was accepted without aspect_ratio")
	}
}

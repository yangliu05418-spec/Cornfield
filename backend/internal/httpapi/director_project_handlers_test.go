package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidDirectorProjectName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{"导演台 01", true},
		{"", false},
		{strings.Repeat("界", 64), true},
		{strings.Repeat("界", 65), false},
	} {
		if got := validDirectorProjectName(test.name); got != test.want {
			t.Fatalf("validDirectorProjectName(%q)=%v, want %v", test.name, got, test.want)
		}
	}
}

func TestDirectorDocumentBounds(t *testing.T) {
	t.Parallel()
	ownerID := uuid.MustParse("5d4427a8-57e4-4f37-bf15-caf6d2fc5e64")
	if validateDirectorDocument(bytes.Repeat([]byte(" "), maxDirectorDocumentBytes+1), ownerID) == nil {
		t.Fatal("oversized document was accepted")
	}
	var document map[string]any
	if err := json.Unmarshal(validDirectorDocumentFixture(), &document); err != nil {
		t.Fatal(err)
	}
	leaf := map[string]any{"value": true}
	for range maxDirectorDocumentDepth + 1 {
		leaf = map[string]any{"extension": leaf}
	}
	document["safeExtension"] = leaf
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if validateDirectorDocument(encoded, ownerID) == nil {
		t.Fatal("excessively nested document was accepted")
	}
}

func TestValidDirectorDocument(t *testing.T) {
	t.Parallel()
	ownerID := uuid.MustParse("5d4427a8-57e4-4f37-bf15-caf6d2fc5e64")
	valid := validDirectorDocumentFixture()
	if err := validateDirectorDocument(valid, ownerID); err != nil {
		t.Fatalf("valid document was rejected: %v", err)
	}
	for _, value := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{"broken"`)} {
		if validateDirectorDocument(value, ownerID) == nil {
			t.Fatalf("invalid document was accepted: %q", value)
		}
	}
}

func TestDirectorDocumentRejectsUnsafeOrCrossTenantAssets(t *testing.T) {
	t.Parallel()
	ownerID := uuid.MustParse("5d4427a8-57e4-4f37-bf15-caf6d2fc5e64")
	otherID := uuid.MustParse("84d9b9f4-b8c3-42e1-8875-3ad1d094bc65")
	for name, asset := range map[string]map[string]any{
		"data URL": {
			"id": "asset", "fileName": "asset.glb", "url": "data:model/gltf-binary;base64,AAAA",
		},
		"blob URL": {
			"id": "asset", "fileName": "asset.glb", "url": "blob:https://example.test/id",
		},
		"unsupported protocol": {
			"id": "asset", "fileName": "asset.glb", "url": "file:///private/asset.glb",
		},
		"cross tenant": {
			"id": "asset", "fileName": "asset.glb", "assetSource": "local",
			"storageKey": "user:" + otherID.String() + ":asset",
			"url":        "director-asset://local/user%3A" + otherID.String() + "%3Aasset",
		},
		"unscoped local URL": {
			"id": "asset", "fileName": "asset.glb", "assetSource": "local",
			"url": "director-asset://local/asset",
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := directorDocumentWithAssets(t, []map[string]any{asset})
			if validateDirectorDocument(document, ownerID) == nil {
				t.Fatal("unsafe document was accepted")
			}
		})
	}
}

func TestDirectorDocumentAcceptsCurrentTenantLocalAsset(t *testing.T) {
	t.Parallel()
	ownerID := uuid.MustParse("5d4427a8-57e4-4f37-bf15-caf6d2fc5e64")
	key := "user:" + ownerID.String() + ":asset"
	document := directorDocumentWithAssets(t, []map[string]any{{
		"id": "asset", "fileName": "asset.glb", "assetSource": "local",
		"storageKey": key,
		"url":        "director-asset://local/user%3A" + ownerID.String() + "%3Aasset",
	}})
	if err := validateDirectorDocument(document, ownerID); err != nil {
		t.Fatalf("current-tenant asset was rejected: %v", err)
	}
}

func TestDirectorDocumentAcceptsBuiltInLibraryAsset(t *testing.T) {
	t.Parallel()
	ownerID := uuid.MustParse("5d4427a8-57e4-4f37-bf15-caf6d2fc5e64")
	document := directorDocumentWithAssets(t, []map[string]any{{
		"id": "builtin:ATM_low.fbx", "fileName": "ATM_low.fbx", "assetSource": "library",
		"url": "builtin://life/ATM_low.fbx",
	}})
	if err := validateDirectorDocument(document, ownerID); err != nil {
		t.Fatalf("built-in library asset was rejected: %v", err)
	}
}

func validDirectorDocumentFixture() json.RawMessage {
	return json.RawMessage(`{
		"format":"3d-director-desk-project",
		"schemaVersion":1,
		"project":{
			"version":1,
			"scene":{"backgroundColor":"#0f1113"},
			"assets":[],
			"animationAssets":[],
			"objects":[],
			"cameras":[{
				"id":"camera-1","name":"Camera 1","fov":50,
				"transform":{"position":[0,1,2],"rotation":[0,0,0],"scale":[1,1,1]},
				"target":[0,0,0]
			}],
			"activeCameraId":"camera-1",
			"panoramaAssetId":null
		}
	}`)
}

func directorDocumentWithAssets(t *testing.T, assets []map[string]any) json.RawMessage {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(validDirectorDocumentFixture(), &document); err != nil {
		t.Fatal(err)
	}
	document["project"].(map[string]any)["assets"] = assets
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

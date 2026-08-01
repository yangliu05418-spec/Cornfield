package httpapi

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const (
	maxDirectorDocumentDepth      = 24
	maxDirectorDocumentNodes      = 100_000
	maxDirectorStringBytes        = 16_384
	maxDirectorAssets             = 2_000
	maxDirectorObjects            = 5_000
	maxDirectorCameras            = 500
	maxDirectorAnimationAssets    = 1_000
	maxDirectorKeyframesPerEntity = 2_000
)

var errInvalidDirectorDocument = errors.New("invalid director document")

func validateDirectorDocument(document json.RawMessage, ownerID uuid.UUID) error {
	if len(document) == 0 || len(document) > maxDirectorDocumentBytes {
		return errInvalidDirectorDocument
	}
	var root any
	if err := json.Unmarshal(document, &root); err != nil {
		return errInvalidDirectorDocument
	}
	nodes := 0
	if err := validateDirectorJSONValue(root, 0, &nodes, ""); err != nil {
		return err
	}
	envelope, ok := root.(map[string]any)
	if !ok || envelope["format"] != "3d-director-desk-project" || envelope["schemaVersion"] != float64(1) {
		return errInvalidDirectorDocument
	}
	project, ok := envelope["project"].(map[string]any)
	if !ok || project["version"] != float64(1) {
		return errInvalidDirectorDocument
	}
	if err := validateDirectorProject(project, ownerID); err != nil {
		return err
	}
	return nil
}

func validateDirectorJSONValue(value any, depth int, nodes *int, key string) error {
	if depth > maxDirectorDocumentDepth {
		return errInvalidDirectorDocument
	}
	(*nodes)++
	if *nodes > maxDirectorDocumentNodes {
		return errInvalidDirectorDocument
	}
	switch item := value.(type) {
	case string:
		if len(item) > maxDirectorStringBytes {
			return errInvalidDirectorDocument
		}
		lower := strings.ToLower(strings.TrimSpace(item))
		if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") {
			return errInvalidDirectorDocument
		}
		if strings.EqualFold(key, "url") || strings.HasSuffix(strings.ToLower(key), "url") {
			if !validDirectorAssetURL(item) {
				return errInvalidDirectorDocument
			}
		}
	case []any:
		if len(item) > maxDirectorDocumentNodes {
			return errInvalidDirectorDocument
		}
		for _, child := range item {
			if err := validateDirectorJSONValue(child, depth+1, nodes, key); err != nil {
				return err
			}
		}
	case map[string]any:
		for childKey, child := range item {
			if len(childKey) == 0 || len(childKey) > 256 {
				return errInvalidDirectorDocument
			}
			if err := validateDirectorJSONValue(child, depth+1, nodes, childKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDirectorProject(project map[string]any, ownerID uuid.UUID) error {
	scene, ok := project["scene"].(map[string]any)
	if !ok || !validDirectorString(scene["backgroundColor"], 128) {
		return errInvalidDirectorDocument
	}
	assets, ok := project["assets"].([]any)
	if !ok || len(assets) > maxDirectorAssets {
		return errInvalidDirectorDocument
	}
	for _, value := range assets {
		if err := validateDirectorAsset(value, ownerID); err != nil {
			return err
		}
	}
	if animationValue, exists := project["animationAssets"]; exists {
		animations, ok := animationValue.([]any)
		if !ok || len(animations) > maxDirectorAnimationAssets {
			return errInvalidDirectorDocument
		}
		for _, value := range animations {
			if err := validateDirectorAsset(value, ownerID); err != nil {
				return err
			}
		}
	}
	objects, ok := project["objects"].([]any)
	if !ok || len(objects) > maxDirectorObjects {
		return errInvalidDirectorDocument
	}
	for _, value := range objects {
		item, ok := value.(map[string]any)
		if !ok || !validDirectorIdentity(item) || !validDirectorTransform(item["transform"]) {
			return errInvalidDirectorDocument
		}
		if err := validateDirectorMotionPath(item["motionPath"], true); err != nil {
			return err
		}
	}
	cameras, ok := project["cameras"].([]any)
	if !ok || len(cameras) == 0 || len(cameras) > maxDirectorCameras {
		return errInvalidDirectorDocument
	}
	for _, value := range cameras {
		item, ok := value.(map[string]any)
		if !ok || !validDirectorString(item["id"], 256) || !validDirectorString(item["name"], 512) || !validDirectorTransform(item["transform"]) || !validDirectorNumberTuple(item["target"], 3) {
			return errInvalidDirectorDocument
		}
		if _, ok := item["fov"].(float64); !ok {
			return errInvalidDirectorDocument
		}
		if err := validateDirectorMotionPath(item["motionPath"], false); err != nil {
			return err
		}
	}
	if _, exists := project["activeCameraId"]; !exists {
		return errInvalidDirectorDocument
	}
	if _, exists := project["panoramaAssetId"]; !exists {
		return errInvalidDirectorDocument
	}
	return nil
}

func validateDirectorAsset(value any, ownerID uuid.UUID) error {
	asset, ok := value.(map[string]any)
	if !ok || !validDirectorString(asset["id"], 256) || !validDirectorString(asset["fileName"], 512) {
		return errInvalidDirectorDocument
	}
	rawURL, ok := asset["url"].(string)
	if !ok || !validDirectorAssetURL(rawURL) {
		return errInvalidDirectorDocument
	}
	storageKey, _ := asset["storageKey"].(string)
	const localPrefix = "director-asset://local/"
	if storageKey == "" {
		if strings.HasPrefix(rawURL, localPrefix) || asset["assetSource"] == "local" {
			return errInvalidDirectorDocument
		}
		return nil
	}
	prefix := "user:" + ownerID.String() + ":"
	if ownerID == uuid.Nil || !strings.HasPrefix(storageKey, prefix) || len(storageKey) <= len(prefix) {
		return errInvalidDirectorDocument
	}
	if !strings.HasPrefix(rawURL, localPrefix) {
		return errInvalidDirectorDocument
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(rawURL, localPrefix))
	if err != nil || decoded != storageKey {
		return errInvalidDirectorDocument
	}
	return nil
}

func validDirectorAssetURL(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return true
	}
	if strings.HasPrefix(value, "director-asset://local/") {
		return len(value) > len("director-asset://local/")
	}
	if strings.HasPrefix(value, "builtin://life/") {
		return len(value) > len("builtin://life/")
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func validDirectorIdentity(value map[string]any) bool {
	return validDirectorString(value["id"], 256) && validDirectorString(value["name"], 512) && validDirectorString(value["kind"], 64)
}

func validDirectorString(value any, max int) bool {
	text, ok := value.(string)
	return ok && text != "" && len(text) <= max
}

func validDirectorTransform(value any) bool {
	transform, ok := value.(map[string]any)
	return ok && validDirectorNumberTuple(transform["position"], 3) && validDirectorNumberTuple(transform["rotation"], 3) && validDirectorNumberTuple(transform["scale"], 3)
}

func validDirectorNumberTuple(value any, size int) bool {
	items, ok := value.([]any)
	if !ok || len(items) != size {
		return false
	}
	for _, item := range items {
		if _, ok := item.(float64); !ok {
			return false
		}
	}
	return true
}

func validateDirectorMotionPath(value any, objectPath bool) error {
	if value == nil {
		return nil
	}
	path, ok := value.(map[string]any)
	if !ok {
		return errInvalidDirectorDocument
	}
	keyframes, ok := path["keyframes"].([]any)
	if !ok || len(keyframes) > maxDirectorKeyframesPerEntity {
		return errInvalidDirectorDocument
	}
	for _, value := range keyframes {
		keyframe, ok := value.(map[string]any)
		if !ok || !validDirectorString(keyframe["id"], 256) {
			return errInvalidDirectorDocument
		}
		if _, ok := keyframe["time"].(float64); !ok {
			return errInvalidDirectorDocument
		}
		if objectPath {
			if !validDirectorTransform(keyframe["transform"]) {
				return errInvalidDirectorDocument
			}
		} else if !validDirectorNumberTuple(keyframe["position"], 3) || !validDirectorNumberTuple(keyframe["target"], 3) {
			return errInvalidDirectorDocument
		}
	}
	return nil
}

package httpapi

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"internal-image-studio/internal/promptrefiner"
	"internal-image-studio/internal/provider"
	"internal-image-studio/internal/providerurl"
)

const (
	maxRefinerBodyBytes = 128 << 10
	maxRefinerRunes     = 32_768
)

type promptDiagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Used     int    `json:"used,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type promptRefineResponse struct {
	PolicyVersion string                  `json:"policy_version"`
	Status        string                  `json:"status"`
	Segments      []promptrefiner.Segment `json:"segments"`
	Findings      []promptrefiner.Finding `json:"findings"`
	Diagnostics   []promptDiagnostic      `json:"diagnostics"`
}

func (s *Server) refinePrompt(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRefinerBodyBytes)
	var input generationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if utf8.RuneCountInString(input.Prompt) > maxRefinerRunes {
		writeError(w, http.StatusRequestEntityTooLarge, "PROMPT_TOO_LARGE", "提示词最多可检查 32,768 个字符", false, r)
		return
	}
	model, ok := s.catalog.Find(input.ModelID)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "MODEL_UNAVAILABLE", "所选模型不可用", false, r)
		return
	}
	if input.CapabilityRevision != s.catalog.Hash {
		writeError(w, http.StatusConflict, "CAPABILITY_STALE", "模型能力已更新，请刷新后重试", false, r)
		return
	}

	result := s.promptRefiner.Refine(input.Prompt)
	diagnostics := make([]promptDiagnostic, 0, 4)
	normalized := input
	if err := normalizeGenerationOptions(model.ID, model.Provider, model.Capabilities.MidjourneyVersions, model.Capabilities.Qualities, len(input.InputAssetIDs), &normalized); err != nil {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "CAPABILITY_INVALID", Severity: "warning", Message: "当前模型参数需要调整：" + err.Error()})
	}
	allowedRatios := model.AspectRatiosForResolution(normalized.Resolution)
	ratioValid := len(allowedRatios) == 0 && normalized.AspectRatio == "auto" || slices.Contains(allowedRatios, normalized.AspectRatio)
	resolutionValid := len(model.Capabilities.Resolutions) == 0 && normalized.Resolution == "auto" || slices.Contains(model.Capabilities.Resolutions, normalized.Resolution) || model.Provider == "legnext" && len(model.Capabilities.MidjourneyVersions) > 0
	if !ratioValid || !resolutionValid || normalized.DrawCount < model.Capabilities.DrawCount.Min || normalized.DrawCount > model.Capabilities.DrawCount.Max {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "CAPABILITY_INVALID", Severity: "warning", Message: "画幅、分辨率或抽卡次数不在当前模型支持范围内"})
	}
	if len(normalized.InputAssetIDs) > model.Capabilities.MaxReferenceImages || len(normalized.InputAssetIDs) > 0 && !model.Capabilities.ImageToImage || hasDuplicateAssetIDs(normalized.InputAssetIDs) {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "REFERENCE_INVALID", Severity: "warning", Message: "参考图数量或能力不受当前模型支持"})
	}
	if model.Provider == "legnext" && containsControlledLegnextInput(input.Prompt) {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "CONTROLLED_PROVIDER_INPUT", Severity: "warning", Message: "请移除原始 Midjourney 参数、外部图片链接或花括号结构；这些内容由 Cornfield 统一生成"})
	}

	canonical := provider.CanonicalRequest{
		Model: model.ProviderModel, Prompt: strings.TrimSpace(input.Prompt), AspectRatio: normalized.AspectRatio,
		PromptAspectRatio: model.PromptAspectRatio, Resolution: normalized.Resolution, ExpectedImages: model.OutputsPerDraw,
		RequestParameters: append([]string(nil), model.RequestParameters...), Options: normalized.Options,
	}
	if model.PromptSuffix != "" {
		canonical.Prompt += " " + model.PromptSuffix
	}
	if model.Provider == "legnext" && len(normalized.InputAssetIDs) > 0 {
		references, err := s.refinerReferenceURLs(r, normalized.InputAssetIDs)
		if err != nil {
			diagnostics = append(diagnostics, promptDiagnostic{Code: "REFERENCE_UNAVAILABLE", Severity: "warning", Message: "部分参考图已不可用，请重新选择"})
		} else {
			canonical.ReferenceURLs = references
		}
	}
	finalPrompt := canonical.Prompt
	switch model.Provider {
	case "legnext":
		if value, err := provider.BuildLegnextPrompt(canonical); err == nil {
			finalPrompt = value
		}
	case "openrouter":
		finalPrompt = provider.BuildOpenRouterPrompt(canonical)
	}
	finalLength := utf8.RuneCountInString(finalPrompt)
	if finalLength > 8192 {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "PROMPT_TOO_LONG", Severity: "warning", Message: "拼接模型参数后的提示词超过 Cornfield 生成上限", Used: finalLength, Limit: 8192})
	}
	if model.Provider == "legnext" && finalLength > 1024 {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "MIDJOURNEY_COMPATIBILITY_LIMIT", Severity: "warning", Message: "最终提示词超过 Midjourney 兼容长度，建议精简后再生成", Used: finalLength, Limit: 1024})
	}
	status := result.Status
	if len(diagnostics) > 0 {
		status = "findings"
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, promptRefineResponse{
		PolicyVersion: result.PolicyVersion, Status: status, Segments: result.Segments,
		Findings: result.Findings, Diagnostics: diagnostics,
	})
}

func (s *Server) refinerReferenceURLs(r *http.Request, ids []uuid.UUID) ([]string, error) {
	sess := currentSession(r)
	urls := make([]string, 0, len(ids))
	for _, id := range ids {
		var storageKey string
		err := s.db.QueryRow(r.Context(), `SELECT storage_key FROM assets
			WHERE id=$1 AND owner_user_id=$2 AND purge_pending=false AND purged_at IS NULL AND expires_at>now()`, id, sess.UserID).Scan(&storageKey)
		if err != nil {
			return nil, err
		}
		value, err := providerurl.Sign(s.cfg.PublicURL, s.cfg.ProviderURLSigningSecret, id, filepath.Ext(storageKey), time.Now().Add(time.Hour))
		if err != nil {
			return nil, err
		}
		urls = append(urls, value)
	}
	return urls, nil
}

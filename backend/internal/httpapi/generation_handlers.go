package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"internal-image-studio/internal/provider"
)

type generationRequest struct {
	ModelID            string                     `json:"model_id"`
	CapabilityRevision string                     `json:"capability_revision"`
	Prompt             string                     `json:"prompt"`
	AspectRatio        string                     `json:"aspect_ratio"`
	Resolution         string                     `json:"resolution"`
	DrawCount          int                        `json:"draw_count"`
	InputAssetIDs      []uuid.UUID                `json:"input_asset_ids"`
	Options            provider.GenerationOptions `json:"options"`
}

func normalizeGenerationOptions(modelID, providerID string, versions, qualities []string, inputCount int, input *generationRequest) error {
	if providerID != "legnext" {
		if input.Options.Midjourney != nil {
			return errors.New("Midjourney options are not supported by this model")
		}
		if len(qualities) == 0 {
			if input.Options.Image != nil {
				return errors.New("image quality is not supported by this model")
			}
		} else {
			if input.Options.Image == nil {
				input.Options.Image = &provider.ImageOptions{Quality: qualities[0]}
			}
			if !slices.Contains(qualities, input.Options.Image.Quality) {
				return errors.New("image quality is outside the supported range")
			}
		}
		if input.AspectRatio == "" {
			input.AspectRatio = "auto"
		}
		if input.Resolution == "" {
			input.Resolution = "auto"
		}
		return nil
	}
	if len(versions) == 0 {
		return nil
	}
	options := input.Options.Midjourney
	if options == nil {
		options = &provider.MidjourneyOptions{Version: versions[0], Speed: "fast", Stylize: 100}
		input.Options.Midjourney = options
	}
	if !slices.Contains(versions, options.Version) || options.Stylize < 0 || options.Stylize > 1000 || options.Chaos < 0 || options.Chaos > 100 || options.Weird < 0 || options.Weird > 3000 {
		return errors.New("Midjourney options are outside the supported range")
	}
	if options.ImageWeight != nil && (inputCount == 0 || *options.ImageWeight < 0 || *options.ImageWeight > 3) {
		return errors.New("Midjourney image weight requires a reference image and must be between 0 and 3")
	}
	qualityAllowed := func(values ...float64) bool {
		if options.Quality == nil {
			return false
		}
		return slices.Contains(values, *options.Quality)
	}
	switch options.Version {
	case "8.1":
		if options.Resolution == "" {
			options.Resolution = "sd"
		}
		if options.Speed == "" {
			options.Speed = "fast"
		}
		if (options.Resolution != "sd" && options.Resolution != "hd") || options.Speed != "fast" || options.Quality != nil || options.Draft {
			return errors.New("Midjourney V8.1 option combination is unsupported")
		}
		input.Resolution = strings.ToUpper(options.Resolution)
	case "8":
		if options.Resolution == "" {
			options.Resolution = "sd"
		}
		if options.Speed == "" {
			options.Speed = "fast"
		}
		if (options.Resolution != "sd" && options.Resolution != "hd") || options.Speed != "fast" || options.Draft {
			return errors.New("Midjourney V8 option combination is unsupported")
		}
		if options.Quality == nil {
			quality := 1.0
			options.Quality = &quality
		}
		if !qualityAllowed(1, 4) {
			return errors.New("Midjourney V8 quality is unsupported")
		}
		input.Resolution = strings.ToUpper(options.Resolution)
	case "7":
		if options.Speed == "" {
			options.Speed = "fast"
		}
		if options.Speed != "fast" && options.Speed != "turbo" {
			return errors.New("Midjourney V7 speed is unsupported")
		}
		if options.Draft {
			options.Quality = nil
		} else if options.Quality == nil {
			quality := 1.0
			options.Quality = &quality
		}
		if options.Quality != nil && !qualityAllowed(1, 2, 4) {
			return errors.New("Midjourney V7 quality is unsupported")
		}
		options.Resolution = ""
		input.Resolution = "auto"
	case "6", "6.1", "niji 6":
		if options.Speed == "" {
			options.Speed = "fast"
		}
		if options.Speed != "fast" && options.Speed != "turbo" {
			return errors.New("Midjourney legacy speed is unsupported")
		}
		if options.Draft {
			return errors.New("Midjourney legacy versions do not support Draft mode")
		}
		if options.Quality == nil {
			quality := 1.0
			options.Quality = &quality
		}
		if !qualityAllowed(0.5, 1, 2) {
			return errors.New("Midjourney legacy quality is unsupported")
		}
		options.Resolution = ""
		input.Resolution = "auto"
	default:
		return errors.New("Midjourney version is unsupported")
	}
	if input.DrawCount != 1 || modelID != "legnext-midjourney" {
		return errors.New("Midjourney creates exactly one four-image draw")
	}
	return nil
}

const (
	generationBurstCapacity = 4.0
	generationRefillPerSec  = 12.0 / 60.0
)

var (
	errGenerationReferenceUnavailable = errors.New("generation reference is unavailable")
	errGenerationReferenceTooLarge    = errors.New("generation reference exceeds the model byte limit")
)

func hasDuplicateAssetIDs(ids []uuid.UUID) bool {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func referenceExceedsModelLimit(byteSize, maxReferenceBytes int64) bool {
	return maxReferenceBytes < 1 || byteSize > maxReferenceBytes
}

func lockUsableInputAssetIDs(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID, ids []uuid.UUID, maxReferenceBytes int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id,byte_size,
		(owner_user_id=$1 AND purged_at IS NULL AND purge_pending=false AND expires_at>now()) AS usable
		FROM assets WHERE id=ANY($2)
		ORDER BY id FOR SHARE`, ownerID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	unavailable := false
	tooLarge := false
	for rows.Next() {
		var id uuid.UUID
		var byteSize int64
		var usable bool
		if err = rows.Scan(&id, &byteSize, &usable); err != nil {
			return err
		}
		count++
		unavailable = unavailable || !usable
		tooLarge = tooLarge || referenceExceedsModelLimit(byteSize, maxReferenceBytes)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	// Availability takes precedence so a caller cannot distinguish a missing
	// or foreign asset from any property of that asset.
	if count != len(ids) || unavailable {
		return errGenerationReferenceUnavailable
	}
	if tooLarge {
		return errGenerationReferenceTooLarge
	}
	return nil
}

func lockUsableBatchInputAssets(ctx context.Context, tx pgx.Tx, ownerID, batchID uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT a.id,
		(a.owner_user_id=$2 AND a.purged_at IS NULL AND a.purge_pending=false AND a.expires_at>now()) AS usable
		FROM generation_input_assets input
		JOIN assets a ON a.id=input.asset_id
		WHERE input.batch_id=$1
		ORDER BY a.id FOR SHARE OF a`, batchID, ownerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var usable bool
		if err = rows.Scan(&id, &usable); err != nil {
			return err
		}
		if !usable {
			return errGenerationReferenceUnavailable
		}
	}
	return rows.Err()
}

func generationRequestHash(input generationRequest) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return stringHex(digest[:]), nil
}

func stringHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = alphabet[item>>4]
		encoded[index*2+1] = alphabet[item&0x0f]
	}
	return string(encoded)
}

func refillGenerationTokens(tokens float64, updatedAt, now time.Time) float64 {
	if now.Before(updatedAt) {
		return tokens
	}
	tokens += now.Sub(updatedAt).Seconds() * generationRefillPerSec
	if tokens > generationBurstCapacity {
		return generationBurstCapacity
	}
	return tokens
}

func containsControlledLegnextInput(prompt string) bool {
	for _, field := range strings.Fields(strings.ToLower(prompt)) {
		field = strings.Trim(field, "\"'`()[]{}<>,.;")
		// Cornfield owns every Midjourney switch in V1. Rejecting the entire
		// provider flag namespace prevents aliases and newly introduced flags
		// from bypassing the model capability snapshot. External image URLs are
		// also rejected so references can only enter through validated assets.
		if strings.HasPrefix(field, "--") || strings.Contains(field, "https://") || strings.Contains(field, "http://") {
			return true
		}
	}
	return false
}

type jobResponse struct {
	ID              uuid.UUID                  `json:"id"`
	DrawIndex       int                        `json:"draw_index"`
	Status          string                     `json:"status"`
	ExpectedOutputs int                        `json:"expected_outputs"`
	ErrorCode       *string                    `json:"error_code,omitempty"`
	ErrorMessage    *string                    `json:"error_message,omitempty"`
	Outputs         []generationOutputResponse `json:"outputs"`
	DeletedOutputs  []int                      `json:"deleted_outputs,omitempty"`
}

type generationOutputResponse struct {
	AssetID      uuid.UUID `json:"asset_id"`
	OutputIndex  int       `json:"output_index"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	MediaType    string    `json:"media_type"`
	URL          string    `json:"url"`
	Thumb320URL  string    `json:"thumb_320_url"`
	Thumb640URL  string    `json:"thumb_640_url"`
	Thumb1280URL string    `json:"thumb_1280_url"`
}

type batchResponse struct {
	ID               uuid.UUID                  `json:"id"`
	ModelID          string                     `json:"model_id"`
	Prompt           string                     `json:"prompt"`
	AspectRatio      string                     `json:"aspect_ratio"`
	Resolution       string                     `json:"resolution"`
	DrawCount        int                        `json:"draw_count"`
	ExpectedOutputs  int                        `json:"expected_outputs"`
	CompletedOutputs int                        `json:"completed_outputs"`
	Status           string                     `json:"status"`
	CreatedAt        time.Time                  `json:"created_at"`
	Jobs             []jobResponse              `json:"jobs"`
	Options          provider.GenerationOptions `json:"options"`
}

type generationJobLocation struct {
	batchIndex int
	jobIndex   int
}

type generationAssembler struct {
	items        []batchResponse
	batchIndexes map[uuid.UUID]int
	jobIndexes   map[uuid.UUID]generationJobLocation
}

func newGenerationAssembler(items []batchResponse) *generationAssembler {
	assembler := &generationAssembler{
		items:        items,
		batchIndexes: make(map[uuid.UUID]int, len(items)),
		jobIndexes:   make(map[uuid.UUID]generationJobLocation),
	}
	for index := range assembler.items {
		assembler.items[index].Jobs = make([]jobResponse, 0, assembler.items[index].DrawCount)
		assembler.batchIndexes[assembler.items[index].ID] = index
	}
	return assembler
}

func (a *generationAssembler) addJob(batchID uuid.UUID, job jobResponse) bool {
	batchIndex, ok := a.batchIndexes[batchID]
	if !ok {
		return false
	}
	job.Outputs = make([]generationOutputResponse, 0, job.ExpectedOutputs)
	jobIndex := len(a.items[batchIndex].Jobs)
	a.items[batchIndex].Jobs = append(a.items[batchIndex].Jobs, job)
	a.jobIndexes[job.ID] = generationJobLocation{batchIndex: batchIndex, jobIndex: jobIndex}
	return true
}

func (a *generationAssembler) addOutput(jobID uuid.UUID, output generationOutputResponse) bool {
	location, ok := a.jobIndexes[jobID]
	if !ok {
		return false
	}
	a.items[location.batchIndex].Jobs[location.jobIndex].Outputs = append(a.items[location.batchIndex].Jobs[location.jobIndex].Outputs, output)
	return true
}

func (a *generationAssembler) addDeletedOutput(jobID uuid.UUID, outputIndex int) {
	if location, ok := a.jobIndexes[jobID]; ok {
		a.items[location.batchIndex].Jobs[location.jobIndex].DeletedOutputs = append(a.items[location.batchIndex].Jobs[location.jobIndex].DeletedOutputs, outputIndex)
	}
}

type generationCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

func encodeGenerationCursor(createdAt time.Time, id uuid.UUID) string {
	payload, _ := json.Marshal(generationCursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeGenerationCursor(raw string) (*time.Time, uuid.UUID, error) {
	if raw == "" {
		return nil, uuid.Nil, nil
	}
	if len(raw) > 512 {
		return nil, uuid.Nil, errInvalidGenerationCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, uuid.Nil, err
	}
	var cursor generationCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == uuid.Nil || cursor.CreatedAt.IsZero() {
		return nil, uuid.Nil, errInvalidGenerationCursor
	}
	return &cursor.CreatedAt, cursor.ID, nil
}

func finishGenerationPage(items []batchResponse, limit int) ([]batchResponse, string) {
	if len(items) <= limit {
		return items, ""
	}
	items = items[:limit]
	last := items[len(items)-1]
	return items, encodeGenerationCursor(last.CreatedAt, last.ID)
}

func setGenerationOutputURLs(output *generationOutputResponse) {
	base := "/api/v1/assets/" + output.AssetID.String() + "/content"
	output.URL = base
	output.Thumb320URL = base + "?variant=320"
	output.Thumb640URL = base + "?variant=640"
	output.Thumb1280URL = base + "?variant=1280"
}

var errInvalidGenerationCursor = errors.New("invalid generation cursor")

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	items := make([]any, 0, len(s.catalog.Models))
	for _, model := range s.catalog.Models {
		if !model.Enabled {
			continue
		}
		items = append(items, map[string]any{
			"id": model.ID, "display_name": model.DisplayName, "provider": model.Provider,
			"outputs_per_draw": model.OutputsPerDraw, "capabilities": model.Capabilities,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": s.catalog.Hash, "models": items})
}

func (s *Server) createGeneration(w http.ResponseWriter, r *http.Request) {
	free, err := storageFreePercent(s.cfg.AssetRoot)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "存储状态无法确认，已暂停新的生成任务", true, r)
		return
	}
	if free < 15 {
		writeError(w, http.StatusServiceUnavailable, "DISK_PRESSURE", "存储空间不足，已暂停新的生成任务", true, r)
		return
	}
	var input generationRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	model, ok := s.catalog.Find(input.ModelID)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "MODEL_UNAVAILABLE", "所选模型不可用", false, r)
		return
	}
	if input.CapabilityRevision != s.catalog.Hash {
		writeError(w, http.StatusConflict, "CAPABILITY_STALE", "模型能力已更新，请刷新后重试", false, r)
		return
	}
	promptLength := utf8.RuneCountInString(input.Prompt)
	if err := normalizeGenerationOptions(model.ID, model.Provider, model.Capabilities.MidjourneyVersions, model.Capabilities.Qualities, len(input.InputAssetIDs), &input); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "CAPABILITY_INVALID", err.Error(), false, r)
		return
	}
	ratioValid := len(model.Capabilities.AspectRatios) == 0 && input.AspectRatio == "auto" || slices.Contains(model.Capabilities.AspectRatios, input.AspectRatio)
	resolutionValid := len(model.Capabilities.Resolutions) == 0 && input.Resolution == "auto" || slices.Contains(model.Capabilities.Resolutions, input.Resolution) || model.Provider == "legnext" && len(model.Capabilities.MidjourneyVersions) > 0
	if promptLength < 1 || promptLength > 8192 || !ratioValid || !resolutionValid || input.DrawCount < model.Capabilities.DrawCount.Min || input.DrawCount > model.Capabilities.DrawCount.Max {
		writeError(w, http.StatusUnprocessableEntity, "CAPABILITY_INVALID", "生成参数不在模型支持范围内", false, r)
		return
	}
	if model.Provider == "legnext" && containsControlledLegnextInput(input.Prompt) {
		writeError(w, http.StatusUnprocessableEntity, "CONTROLLED_PROVIDER_INPUT", "提示词不能包含 Midjourney 参数或外部图片链接", false, r)
		return
	}
	if len(input.InputAssetIDs) > model.Capabilities.MaxReferenceImages || (len(input.InputAssetIDs) > 0 && !model.Capabilities.ImageToImage) || hasDuplicateAssetIDs(input.InputAssetIDs) {
		writeError(w, http.StatusUnprocessableEntity, "REFERENCE_INVALID", "参考图数量或能力不受当前模型支持", false, r)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "需要有效的 Idempotency-Key", false, r)
		return
	}
	requestHash, err := generationRequestHash(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "REQUEST_HASH_FAILED", "创建任务失败", true, r)
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())

	// This row lock serializes generation creation per user, making the
	// idempotency, token bucket, and queue-cap decisions atomic.
	var lockedUser uuid.UUID
	if err = tx.QueryRow(r.Context(), `SELECT id FROM users WHERE id=$1 AND status='active' FOR UPDATE`, sess.UserID).Scan(&lockedUser); err != nil {
		writeError(w, http.StatusUnauthorized, "USER_INACTIVE", "用户已失效", false, r)
		return
	}
	var existing uuid.UUID
	var existingHash string
	err = tx.QueryRow(r.Context(), `SELECT id,request_hash FROM generation_batches WHERE owner_user_id=$1 AND idempotency_key=$2`, sess.UserID, idempotencyKey).Scan(&existing, &existingHash)
	if err == nil {
		if existingHash != requestHash {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key 已用于另一组参数", false, r)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "DATABASE_ERROR", "读取任务失败", true, r)
			return
		}
		batch, loadErr := s.loadBatch(r.Context(), existing, sess)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
			return
		}
		writeJSON(w, http.StatusOK, batch)
		return
	}
	if !isNotFound(err) {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
		return
	}

	now := time.Now().UTC()
	tag, err := tx.Exec(r.Context(), `INSERT INTO generation_rate_limits(owner_user_id,tokens,updated_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, sess.UserID, generationBurstCapacity-1, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
		return
	}
	if tag.RowsAffected() == 0 {
		var tokens float64
		var updatedAt time.Time
		if err = tx.QueryRow(r.Context(), `SELECT tokens,updated_at FROM generation_rate_limits WHERE owner_user_id=$1 FOR UPDATE`, sess.UserID).Scan(&tokens, &updatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
			return
		}
		tokens = refillGenerationTokens(tokens, updatedAt, now)
		if tokens < 1 {
			writeError(w, http.StatusTooManyRequests, "GENERATION_RATE_LIMIT", "生成请求过于频繁，请稍后再试", true, r)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE generation_rate_limits SET tokens=$2,updated_at=$3 WHERE owner_user_id=$1`, sess.UserID, tokens-1, now); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
			return
		}
	}

	var queued int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM generation_jobs WHERE owner_user_id=$1 AND status='queued'`, sess.UserID).Scan(&queued); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
		return
	}
	if queued+input.DrawCount > 100 {
		writeError(w, http.StatusTooManyRequests, "QUEUE_LIMIT", "当前排队任务已达到上限", true, r)
		return
	}
	if len(input.InputAssetIDs) > 0 {
		if err := lockUsableInputAssetIDs(r.Context(), tx, sess.UserID, input.InputAssetIDs, model.Capabilities.MaxReferenceBytes); err != nil {
			if errors.Is(err, errGenerationReferenceTooLarge) {
				writeError(w, http.StatusUnprocessableEntity, "REFERENCE_TOO_LARGE", "参考图超过当前模型的单图大小限制", false, r)
				return
			}
			writeError(w, http.StatusUnprocessableEntity, "REFERENCE_NOT_FOUND", "部分参考图不存在或不可访问", false, r)
			return
		}
	}
	var batchID uuid.UUID
	expected := input.DrawCount * model.OutputsPerDraw
	err = tx.QueryRow(r.Context(), `INSERT INTO generation_batches(owner_user_id,idempotency_key,request_hash,model_id,capability_revision,prompt,aspect_ratio,resolution,draw_count,expected_outputs,options)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, sess.UserID, idempotencyKey, requestHash, input.ModelID, s.catalog.Hash, input.Prompt, input.AspectRatio, input.Resolution, input.DrawCount, expected, input.Options).Scan(&batchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败，请确认模型配置已应用", true, r)
		return
	}
	for i := 0; i < input.DrawCount; i++ {
		var jobID uuid.UUID
		if err = tx.QueryRow(r.Context(), `INSERT INTO generation_jobs(batch_id,owner_user_id,draw_index,expected_outputs) VALUES($1,$2,$3,$4) RETURNING id`, batchID, sess.UserID, i, model.OutputsPerDraw).Scan(&jobID); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
			return
		}
		payload, _ := json.Marshal(map[string]any{"status": "queued", "draw_index": i, "expected_outputs": model.OutputsPerDraw})
		_, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.queued',$4)`, sess.UserID, batchID, jobID, payload)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
			return
		}
	}
	for i, assetID := range input.InputAssetIDs {
		_, err = tx.Exec(r.Context(), `INSERT INTO generation_input_assets(batch_id,asset_id,position) VALUES($1,$2,$3)`, batchID, assetID, i)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,batch_id,event_type,payload) VALUES($1,$2,'batch.created',jsonb_build_object('status','queued','expected_outputs',$3::integer))`, sess.UserID, batchID, expected); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "创建任务失败", true, r)
		return
	}
	batch, err := s.loadBatch(r.Context(), batchID, sess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "任务已创建但读取失败", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, batch)
}

func (s *Server) listGenerations(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "分页大小必须在 1 到 100 之间", false, r)
			return
		}
		limit = parsed
	}
	cursorTime, cursorID, err := decodeGenerationCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "分页游标无效", false, r)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id,model_id,prompt,aspect_ratio,resolution,draw_count,expected_outputs,completed_outputs,status,created_at,options
		FROM generation_batches WHERE owner_user_id=$1
		  AND ($2::timestamptz IS NULL OR (created_at,id)<($2,$3::uuid))
		ORDER BY created_at DESC,id DESC LIMIT $4`, sess.UserID, cursorTime, cursorID, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	items := make([]batchResponse, 0, limit+1)
	for rows.Next() {
		var item batchResponse
		if err = rows.Scan(&item.ID, &item.ModelID, &item.Prompt, &item.AspectRatio, &item.Resolution, &item.DrawCount, &item.ExpectedOutputs, &item.CompletedOutputs, &item.Status, &item.CreatedAt, &item.Options); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
			return
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	rows.Close()

	items, nextCursor := finishGenerationPage(items, limit)
	assembler := newGenerationAssembler(items)
	if len(items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"items": assembler.items, "next_cursor": nextCursor})
		return
	}
	batchIDs := make([]uuid.UUID, len(items))
	for index := range items {
		batchIDs[index] = items[index].ID
	}

	jobRows, err := s.db.Query(r.Context(), `SELECT batch_id,id,draw_index,status,expected_outputs,error_code,error_message
		FROM generation_jobs WHERE batch_id=ANY($1) ORDER BY batch_id,draw_index,id`, batchIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	for jobRows.Next() {
		var batchID uuid.UUID
		var job jobResponse
		if err = jobRows.Scan(&batchID, &job.ID, &job.DrawIndex, &job.Status, &job.ExpectedOutputs, &job.ErrorCode, &job.ErrorMessage); err != nil {
			jobRows.Close()
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
			return
		}
		assembler.addJob(batchID, job)
	}
	if err = jobRows.Err(); err != nil {
		jobRows.Close()
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	jobRows.Close()

	outputRows, err := s.db.Query(r.Context(), `SELECT o.job_id,o.asset_id,o.output_index,a.width,a.height,a.media_type,
		(o.deleted_at IS NOT NULL OR a.purged_at IS NOT NULL OR a.purge_pending)
		FROM generation_outputs o JOIN generation_jobs j ON j.id=o.job_id JOIN assets a ON a.id=o.asset_id
		WHERE j.batch_id=ANY($1) ORDER BY j.batch_id,j.draw_index,o.output_index,o.asset_id`, batchIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	for outputRows.Next() {
		var jobID uuid.UUID
		var output generationOutputResponse
		var deleted bool
		if err = outputRows.Scan(&jobID, &output.AssetID, &output.OutputIndex, &output.Width, &output.Height, &output.MediaType, &deleted); err != nil {
			outputRows.Close()
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
			return
		}
		if deleted {
			assembler.addDeletedOutput(jobID, output.OutputIndex)
			continue
		}
		setGenerationOutputURLs(&output)
		assembler.addOutput(jobID, output)
	}
	if err = outputRows.Err(); err != nil {
		outputRows.Close()
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	outputRows.Close()
	writeJSON(w, http.StatusOK, map[string]any{"items": assembler.items, "next_cursor": nextCursor})
}

func (s *Server) getGeneration(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := s.loadBatch(r.Context(), id, currentSession(r))
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "GENERATION_NOT_FOUND", "任务不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取任务失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) loadBatch(ctx context.Context, id uuid.UUID, sess session) (batchResponse, error) {
	var item batchResponse
	err := s.db.QueryRow(ctx, `SELECT id,model_id,prompt,aspect_ratio,resolution,draw_count,expected_outputs,completed_outputs,status,created_at,options
		FROM generation_batches WHERE id=$1 AND (owner_user_id=$2 OR $3='admin')`, id, sess.UserID, sess.Role).Scan(&item.ID, &item.ModelID, &item.Prompt, &item.AspectRatio, &item.Resolution, &item.DrawCount, &item.ExpectedOutputs, &item.CompletedOutputs, &item.Status, &item.CreatedAt, &item.Options)
	if err != nil {
		return item, err
	}
	rows, err := s.db.Query(ctx, `SELECT id,draw_index,status,expected_outputs,error_code,error_message FROM generation_jobs WHERE batch_id=$1 ORDER BY draw_index`, id)
	if err != nil {
		return item, err
	}
	item.Jobs = make([]jobResponse, 0, item.DrawCount)
	jobIndexes := make(map[uuid.UUID]int, item.DrawCount)
	for rows.Next() {
		var job jobResponse
		if err := rows.Scan(&job.ID, &job.DrawIndex, &job.Status, &job.ExpectedOutputs, &job.ErrorCode, &job.ErrorMessage); err != nil {
			rows.Close()
			return item, err
		}
		job.Outputs = make([]generationOutputResponse, 0, job.ExpectedOutputs)
		jobIndexes[job.ID] = len(item.Jobs)
		item.Jobs = append(item.Jobs, job)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return item, err
	}
	rows.Close()
	outputRows, err := s.db.Query(ctx, `SELECT o.job_id,o.asset_id,o.output_index,a.width,a.height,a.media_type,
		(o.deleted_at IS NOT NULL OR a.purged_at IS NOT NULL OR a.purge_pending)
		FROM generation_outputs o JOIN generation_jobs j ON j.id=o.job_id JOIN assets a ON a.id=o.asset_id
		WHERE j.batch_id=$1 ORDER BY j.draw_index,o.output_index`, id)
	if err != nil {
		return item, err
	}
	defer outputRows.Close()
	for outputRows.Next() {
		var jobID uuid.UUID
		var output generationOutputResponse
		var deleted bool
		if err = outputRows.Scan(&jobID, &output.AssetID, &output.OutputIndex, &output.Width, &output.Height, &output.MediaType, &deleted); err != nil {
			return item, err
		}
		if deleted {
			if index, exists := jobIndexes[jobID]; exists {
				item.Jobs[index].DeletedOutputs = append(item.Jobs[index].DeletedOutputs, output.OutputIndex)
			}
			continue
		}
		setGenerationOutputURLs(&output)
		if index, exists := jobIndexes[jobID]; exists {
			item.Jobs[index].Outputs = append(item.Jobs[index].Outputs, output)
		}
	}
	return item, outputRows.Err()
}

func (s *Server) cancelBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	ownerID, _, err := lockOwnedBatch(r.Context(), tx, id, sess)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "GENERATION_NOT_FOUND", "任务不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	// A submitting request may already have been accepted upstream even before
	// a provider job ID is available. Only states that are provably pre-submit
	// can be cancelled locally without a possible provider charge.
	rows, err := tx.Query(r.Context(), `UPDATE generation_jobs SET status=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN 'cancelled' ELSE 'cancelling' END,
		cancel_mode=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN 'local' ELSE 'discard_result_only' END,
		dispatch_state=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN 'finished' ELSE dispatch_state END,
		completed_at=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN now() ELSE completed_at END,updated_at=now()
		WHERE batch_id=$1 AND status NOT IN ('succeeded','failed','submission_uncertain','cancelled','cancelling') RETURNING id,status,cancel_mode`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	type changedJob struct {
		id         uuid.UUID
		status     string
		cancelMode string
	}
	changed := make([]changedJob, 0)
	for rows.Next() {
		var item changedJob
		if err = rows.Scan(&item.id, &item.status, &item.cancelMode); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
			return
		}
		changed = append(changed, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	for _, item := range changed {
		eventType := "job.cancelling"
		if item.status == "cancelled" {
			eventType = "job.cancelled"
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,$4,jsonb_build_object('status',$5::text,'cancel_mode',$6::text,'cost_may_have_been_incurred',$6::text<>'local'))`, ownerID, id, item.id, eventType, item.status, item.cancelMode); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
			return
		}
		if _, err = tx.Exec(r.Context(), `SELECT pg_notify('job_controls',$1)`, item.id.String()); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
			return
		}
	}
	status, err := reconcileHTTPBatch(r.Context(), tx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	if len(changed) == 0 && status != "cancelling" && status != "cancelled" {
		writeError(w, http.StatusConflict, "GENERATION_NOT_CANCELLABLE", "任务已经结束", false, r)
		return
	}
	var costMayHaveBeenIncurred bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM generation_jobs WHERE batch_id=$1 AND cancel_mode='discard_result_only')`, id).Scan(&costMayHaveBeenIncurred); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消任务失败", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "cost_may_have_been_incurred": costMayHaveBeenIncurred})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	batchID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	jobID, ok := parseUUIDParam(w, r, "jobID")
	if !ok {
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	ownerID, _, err := lockOwnedBatch(r.Context(), tx, batchID, sess)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "GENERATION_NOT_FOUND", "任务不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
		return
	}
	var status, cancelMode string
	updated := false
	err = tx.QueryRow(r.Context(), `UPDATE generation_jobs SET status=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN 'cancelled' ELSE 'cancelling' END,
		cancel_mode=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN 'local' ELSE 'discard_result_only' END,
		dispatch_state=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN 'finished' ELSE dispatch_state END,
		completed_at=CASE WHEN provider_job_id IS NULL AND status IN ('queued','dispatched') THEN now() ELSE completed_at END,updated_at=now()
		WHERE id=$1 AND batch_id=$2 AND status NOT IN ('succeeded','failed','submission_uncertain','cancelled','cancelling') RETURNING status,cancel_mode`, jobID, batchID).Scan(&status, &cancelMode)
	if isNotFound(err) {
		if lookupErr := tx.QueryRow(r.Context(), `SELECT status,COALESCE(cancel_mode,'') FROM generation_jobs WHERE id=$1 AND batch_id=$2`, jobID, batchID).Scan(&status, &cancelMode); lookupErr != nil {
			writeError(w, http.StatusNotFound, "JOB_NOT_FOUND", "抽卡任务不存在", false, r)
			return
		}
		if status != "cancelling" && status != "cancelled" {
			writeError(w, http.StatusConflict, "JOB_NOT_CANCELLABLE", "抽卡任务已经结束", false, r)
			return
		}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
		return
	} else {
		updated = true
	}
	if updated {
		eventType := "job.cancelling"
		if status == "cancelled" {
			eventType = "job.cancelled"
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,$4,jsonb_build_object('status',$5::text,'cancel_mode',$6::text,'cost_may_have_been_incurred',$6::text<>'local'))`, ownerID, batchID, jobID, eventType, status, cancelMode); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
			return
		}
		if _, err = tx.Exec(r.Context(), `SELECT pg_notify('job_controls',$1)`, jobID.String()); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
			return
		}
	}
	if _, err = reconcileHTTPBatch(r.Context(), tx, batchID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "取消抽卡失败", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "cancel_mode": cancelMode, "cost_may_have_been_incurred": cancelMode == "discard_result_only"})
}

func (s *Server) retryBatch(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	ownerID, _, err := lockOwnedBatch(r.Context(), tx, id, sess)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "GENERATION_NOT_FOUND", "任务不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	var lockedUser uuid.UUID
	if err = tx.QueryRow(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, ownerID).Scan(&lockedUser); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	if err = lockUsableBatchInputAssets(r.Context(), tx, ownerID, id); err != nil {
		if errors.Is(err, errGenerationReferenceUnavailable) {
			writeError(w, http.StatusConflict, "REFERENCE_UNAVAILABLE", "A reference image has expired or is being removed; this batch cannot be retried", false, r)
		} else {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to validate generation references", true, r)
		}
		return
	}
	var queued, retryable int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM generation_jobs WHERE owner_user_id=$1 AND status='queued'`, ownerID).Scan(&queued); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM generation_jobs j WHERE batch_id=$1 AND status='failed' AND retryable=true AND NOT EXISTS(SELECT 1 FROM generation_outputs o WHERE o.job_id=j.id)`, id).Scan(&retryable); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	if retryable == 0 {
		writeError(w, http.StatusConflict, "NO_RETRYABLE_JOBS", "没有可重试的失败任务", false, r)
		return
	}
	if queued+retryable > 100 {
		writeError(w, http.StatusTooManyRequests, "QUEUE_LIMIT", "当前排队任务已达到上限", true, r)
		return
	}
	rows, err := tx.Query(r.Context(), `UPDATE generation_jobs j SET status='queued',dispatch_state='pending',river_job_id=NULL,provider_job_id=NULL,
		error_code=NULL,error_message=NULL,submission_uncertain=false,retryable=true,next_attempt_at=now(),attempt_count=0,
		generation_deadline=NULL,execution_generation=execution_generation+1,dispatched_at=NULL,started_at=NULL,completed_at=NULL,cancel_mode=NULL,updated_at=now()
		WHERE j.batch_id=$1 AND j.status='failed' AND j.retryable=true AND NOT EXISTS(SELECT 1 FROM generation_outputs o WHERE o.job_id=j.id) RETURNING id`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	retriedIDs := make([]uuid.UUID, 0, retryable)
	for rows.Next() {
		var jobID uuid.UUID
		if err = rows.Scan(&jobID); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
			return
		}
		retriedIDs = append(retriedIDs, jobID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	for _, jobID := range retriedIDs {
		if _, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.queued',jsonb_build_object('status','queued','manual_retry',true))`, ownerID, id, jobID); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
			return
		}
	}
	status, err := reconcileHTTPBatch(r.Context(), tx, id)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "重试任务失败", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "retried_jobs": len(retriedIDs), "duplicate_cost_risk": false})
}

type batchStateCounts struct {
	total      int
	succeeded  int
	failed     int
	cancelled  int
	cancelling int
	running    int
}

func desiredBatchStatus(counts batchStateCounts) string {
	terminal := counts.succeeded + counts.failed + counts.cancelled
	if counts.total > 0 && terminal == counts.total {
		switch {
		case counts.succeeded == counts.total:
			return "succeeded"
		case counts.succeeded > 0:
			return "partial"
		case counts.cancelled == counts.total:
			return "cancelled"
		default:
			return "failed"
		}
	}
	if counts.cancelling > 0 {
		return "cancelling"
	}
	if counts.running > 0 {
		return "running"
	}
	return "queued"
}

func batchStatusWithOutputCount(status string, completed, expected int) string {
	if status == "succeeded" && expected > 0 && completed != expected {
		return "partial"
	}
	return status
}

func lockOwnedBatch(ctx context.Context, tx pgx.Tx, batchID uuid.UUID, sess session) (uuid.UUID, string, error) {
	var ownerID uuid.UUID
	var status string
	err := tx.QueryRow(ctx, `SELECT owner_user_id,status FROM generation_batches WHERE id=$1 AND (owner_user_id=$2 OR $3='admin') FOR UPDATE`, batchID, sess.UserID, sess.Role).Scan(&ownerID, &status)
	return ownerID, status, err
}

func reconcileHTTPBatch(ctx context.Context, tx pgx.Tx, batchID uuid.UUID) (string, error) {
	var ownerID uuid.UUID
	var currentStatus string
	var currentCompleted, expectedOutputs int
	if err := tx.QueryRow(ctx, `SELECT owner_user_id,status,completed_outputs,expected_outputs FROM generation_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&ownerID, &currentStatus, &currentCompleted, &expectedOutputs); err != nil {
		return "", err
	}
	var counts batchStateCounts
	if err := tx.QueryRow(ctx, `SELECT count(*)::int,
		count(*) FILTER (WHERE status='succeeded')::int,
		count(*) FILTER (WHERE status IN ('failed','submission_uncertain'))::int,
		count(*) FILTER (WHERE status='cancelled')::int,
		count(*) FILTER (WHERE status='cancelling')::int,
		count(*) FILTER (WHERE status IN ('dispatched','submitting','provider_pending','ingesting'))::int
		FROM generation_jobs WHERE batch_id=$1`, batchID).Scan(&counts.total, &counts.succeeded, &counts.failed, &counts.cancelled, &counts.cancelling, &counts.running); err != nil {
		return "", err
	}
	var completed int
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM generation_outputs o JOIN generation_jobs j ON j.id=o.job_id WHERE j.batch_id=$1`, batchID).Scan(&completed); err != nil {
		return "", err
	}
	status := batchStatusWithOutputCount(desiredBatchStatus(counts), completed, expectedOutputs)
	if status == currentStatus && completed == currentCompleted {
		return status, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE generation_batches SET status=$2,completed_outputs=$3,updated_at=now() WHERE id=$1`, batchID, status, completed); err != nil {
		return "", err
	}
	eventType := "batch.updated"
	if status == "failed" {
		eventType = "batch.failed"
	} else if status == "succeeded" || status == "partial" || status == "cancelled" {
		eventType = "batch.completed"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,event_type,payload) VALUES($1,$2,$3,jsonb_build_object('status',$4::text,'completed_outputs',$5::integer))`, ownerID, batchID, eventType, status, completed); err != nil {
		return "", err
	}
	return status, nil
}

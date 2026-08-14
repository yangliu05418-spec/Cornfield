package editor

import (
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
)

const (
	RenderRoleContent = "content"
	RenderRoleMask    = "mask"
)

type RenderScene struct {
	Canvas Canvas
	Nodes  []RenderNode
}

type RenderNode struct {
	ID          string
	AssetID     uuid.UUID
	Transform   [6]float64
	Opacity     float64
	Visible     bool
	Order       int
	Crop        *Crop
	Role        string
	MaskNodeID  *string
	BlendMode   string
	Effects     []EffectV2
	ColorMatrix ColorMatrixV1
	ShapeMask   *ShapeMaskV2
	PixelMask   *PixelMaskV2
}

func DecodeRenderScene(raw []byte) (RenderScene, error) {
	document, err := DecodeAny(raw)
	if err != nil {
		return RenderScene{}, err
	}
	return document.RenderScene()
}

func (d DecodedDocument) RenderScene() (RenderScene, error) {
	switch d.SchemaVersion {
	case 1:
		if d.V1 == nil {
			return RenderScene{}, ErrInvalidDocument
		}
		return CompileV1RenderScene(*d.V1)
	case 2:
		if d.V2 == nil {
			return RenderScene{}, ErrInvalidDocument
		}
		return CompileV2RenderScene(*d.V2)
	default:
		return RenderScene{}, ErrInvalidDocument
	}
}

func CompileV1RenderScene(document Document) (RenderScene, error) {
	if err := document.Validate(); err != nil {
		return RenderScene{}, err
	}
	objects := append([]Object(nil), document.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return objects[i].ZIndex < objects[j].ZIndex })
	nodes := make([]RenderNode, len(objects))
	for index, object := range objects {
		nodes[index] = RenderNode{
			ID: object.ID, AssetID: object.AssetID, Transform: object.Transform,
			Opacity: object.Opacity, Visible: object.Visible, Order: index,
			Crop: cloneCrop(object.Crop), Role: RenderRoleContent, BlendMode: "normal", ColorMatrix: IdentityColorMatrixV1(),
		}
	}
	return RenderScene{Canvas: document.Canvas, Nodes: nodes}, nil
}

func CompileV2RenderScene(document DocumentV2) (RenderScene, error) {
	if err := document.Validate(); err != nil {
		return RenderScene{}, err
	}
	byID := make(map[string]NodeV2, len(document.Nodes))
	children := make(map[string][]NodeV2, len(document.Nodes))
	maskIDs := make(map[string]struct{})
	adjustments := make(map[string][]NodeV2)
	for _, node := range document.Nodes {
		if node.Type == "group" && node.BlendMode != "normal" {
			return RenderScene{}, ErrUnsupportedDocumentSemantics
		}
		if node.Type == "group" && node.MaskID != nil {
			return RenderScene{}, ErrUnsupportedDocumentSemantics
		}
		if node.Type == "adjustment" {
			adjustments[*node.TargetID] = append(adjustments[*node.TargetID], node)
		}
		byID[node.ID] = node
		children[parentKey(node.ParentID)] = append(children[parentKey(node.ParentID)], node)
		if node.MaskID != nil {
			maskIDs[*node.MaskID] = struct{}{}
		}
	}
	for _, siblings := range children {
		sort.SliceStable(siblings, func(i, j int) bool {
			if siblings[i].OrderKey == siblings[j].OrderKey {
				return siblings[i].ID < siblings[j].ID
			}
			return siblings[i].OrderKey < siblings[j].OrderKey
		})
	}
	for targetID := range adjustments {
		sort.SliceStable(adjustments[targetID], func(i, j int) bool {
			if adjustments[targetID][i].OrderKey == adjustments[targetID][j].OrderKey {
				return adjustments[targetID][i].ID < adjustments[targetID][j].ID
			}
			return adjustments[targetID][i].OrderKey < adjustments[targetID][j].OrderKey
		})
	}
	for maskID := range maskIDs {
		mask := byID[maskID]
		if mask.MaskID != nil || mask.Crop != nil || mask.ShapeMask != nil || mask.BlendMode != "normal" || hasEnabledEffects(mask.Effects) {
			return RenderScene{}, ErrUnsupportedDocumentSemantics
		}
	}

	nodes := make([]RenderNode, 0, len(document.Nodes))
	var visit func(string, [6]float64, float64, bool)
	visit = func(parent string, parentTransform [6]float64, parentOpacity float64, parentVisible bool) {
		for _, node := range children[parent] {
			transform := MultiplyTransforms(parentTransform, node.Transform)
			opacity := parentOpacity * node.Opacity
			visible := parentVisible && node.Visible
			if node.Type == "group" {
				visit(node.ID, transform, opacity, visible)
				continue
			}
			if node.Type == "adjustment" {
				continue
			}
			role := RenderRoleContent
			if _, isMask := maskIDs[node.ID]; isMask {
				role = RenderRoleMask
			}
			matrices := []ColorMatrixV1{CompileColorMatrixV1(node.Effects)}
			for _, adjustment := range adjustments[node.ID] {
				if adjustment.Visible && adjustment.Opacity > 0 {
					matrices = append(matrices, CompileColorMatrixWithStrengthV1(adjustment.Effects, adjustment.Opacity))
				}
			}
			nodes = append(nodes, RenderNode{
				ID: node.ID, AssetID: *node.AssetID, Transform: transform,
				Opacity: opacity, Visible: visible, Order: len(nodes),
				Crop: cloneCrop(node.Crop), Role: role, MaskNodeID: cloneString(node.MaskID),
				BlendMode: node.BlendMode, Effects: cloneEffects(node.Effects), ColorMatrix: ComposeColorMatricesV1(matrices...),
				ShapeMask: cloneShapeMaskV2(node.ShapeMask),
				PixelMask: clonePixelMaskV2(node.PixelMask),
			})
		}
	}
	visit("", [6]float64{1, 0, 0, 1, 0, 0}, 1, true)
	return RenderScene{Canvas: document.Canvas, Nodes: nodes}, nil
}

func MultiplyTransforms(parent, child [6]float64) [6]float64 {
	return [6]float64{
		parent[0]*child[0] + parent[2]*child[1],
		parent[1]*child[0] + parent[3]*child[1],
		parent[0]*child[2] + parent[2]*child[3],
		parent[1]*child[2] + parent[3]*child[3],
		parent[0]*child[4] + parent[2]*child[5] + parent[4],
		parent[1]*child[4] + parent[3]*child[5] + parent[5],
	}
}

func (s RenderScene) Validate() error {
	if !validCanvas(s.Canvas) || len(s.Nodes) < 1 || len(s.Nodes) > MaxNodesV2 {
		return ErrInvalidDocument
	}
	nodesByID := make(map[string]RenderNode, len(s.Nodes))
	for index, node := range s.Nodes {
		if !validNodeID(node.ID) || node.AssetID == uuid.Nil || !validTransform(node.Transform) ||
			!validOpacity(node.Opacity) || node.Order != index || !validCrop(node.Crop) || !validShapeMaskV2(node.ShapeMask) || !validPixelMaskV2(node.PixelMask) || !validBlendMode(node.BlendMode) || !validEffects(node.Effects) || !validColorMatrixV1(node.ColorMatrix) ||
			(node.Role != RenderRoleContent && node.Role != RenderRoleMask) {
			return ErrInvalidDocument
		}
		if _, exists := nodesByID[node.ID]; exists {
			return ErrInvalidDocument
		}
		if node.Role == RenderRoleMask && (node.MaskNodeID != nil || node.PixelMask != nil) {
			return fmt.Errorf("%w: mask nodes cannot have masks", ErrInvalidDocument)
		}
		nodesByID[node.ID] = node
	}
	for _, node := range s.Nodes {
		if node.MaskNodeID == nil {
			continue
		}
		mask, exists := nodesByID[*node.MaskNodeID]
		if !exists || *node.MaskNodeID == node.ID || node.Role != RenderRoleContent || mask.Role != RenderRoleMask {
			return fmt.Errorf("%w: invalid mask reference", ErrInvalidDocument)
		}
	}
	return nil
}

func validColorMatrixV1(matrix ColorMatrixV1) bool {
	for _, value := range matrix {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1_000_000 {
			return false
		}
	}
	return true
}

func parentKey(parent *string) string {
	if parent == nil {
		return ""
	}
	return *parent
}

func hasEnabledEffects(effects []EffectV2) bool {
	for _, effect := range effects {
		if effect.Enabled {
			return true
		}
	}
	return false
}

func cloneCrop(value *Crop) *Crop {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneShapeMaskV2(value *ShapeMaskV2) *ShapeMaskV2 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func clonePixelMaskV2(value *PixelMaskV2) *PixelMaskV2 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneEffects(value []EffectV2) []EffectV2 {
	if len(value) == 0 {
		return nil
	}
	result := make([]EffectV2, len(value))
	for index, effect := range value {
		result[index] = effect
		result[index].Parameters = make(map[string]float64, len(effect.Parameters))
		for key, parameter := range effect.Parameters {
			result[index].Parameters[key] = parameter
		}
	}
	return result
}

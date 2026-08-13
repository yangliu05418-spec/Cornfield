package provider

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

type Mock struct{}

func (Mock) Submit(_ context.Context, input CanonicalRequest) (Submission, error) {
	result := Result{Status: "completed", Usage: map[string]any{"mock": true, "reference_count": len(input.ReferenceURLs)}}
	for index := 0; index < input.ExpectedImages; index++ {
		canvas := image.NewRGBA(image.Rect(0, 0, 1280, 960))
		for y := 0; y < 960; y++ {
			for x := 0; x < 1280; x++ {
				canvas.SetRGBA(x, y, color.RGBA{R: uint8((x + index*40) % 255), G: uint8((y + 60) % 255), B: uint8((x + y) / 12 % 255), A: 255})
			}
		}
		var output bytes.Buffer
		_ = png.Encode(&output, canvas)
		result.Images = append(result.Images, Image{Bytes: output.Bytes(), MediaType: "image/png"})
	}
	return Submission{ProviderJobID: fmt.Sprintf("mock-%s", input.JobID), Completed: true, Result: result}, nil
}

func (Mock) Poll(context.Context, Submission) (Result, error) { return Result{}, nil }
func (Mock) Cancel(context.Context, Submission) (CancelResult, error) {
	return CancelResult{Accepted: true, Mode: "local"}, nil
}
func (Mock) Probe(context.Context) Health { return Health{Healthy: true, Message: "mock"} }

func (Mock) DecomposeLayers(_ context.Context, input LayerDecompositionRequest) (LayerDecompositionResult, error) {
	base := image.NewRGBA(image.Rect(0, 0, 1280, 960))
	for y := range 960 {
		for x := range 1280 {
			base.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 70, A: 255})
		}
	}
	layer := image.NewRGBA(image.Rect(0, 0, 480, 360))
	for y := range 360 {
		for x := range 480 {
			layer.SetRGBA(x, y, color.RGBA{R: 209, G: 254, B: 23, A: uint8(min(255, x+y))})
		}
	}
	encode := func(value image.Image) []byte {
		var output bytes.Buffer
		_ = png.Encode(&output, value)
		return output.Bytes()
	}
	return LayerDecompositionResult{
		Items: []LayerDecompositionItem{
			{Bytes: encode(base), MediaType: "image/png", Size: "1280x960", ZIndex: 0},
			{Bytes: encode(layer), MediaType: "image/png", Size: "480x360", ZIndex: 1, Name: "主体", Description: "模拟分层主体", BoundingBox: &LayerBoundingBox{Absolute: [4]int{400, 300, 880, 660}, Normalized: [4]float64{312, 312, 687, 687}}},
		},
		Usage: map[string]any{"mock": true, "generated_images": 2, "mode": input.PromptOptimizationMode},
	}, nil
}

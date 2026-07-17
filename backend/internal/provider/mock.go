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

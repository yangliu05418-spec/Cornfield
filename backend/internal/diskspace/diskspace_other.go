//go:build !linux

package diskspace

func FreePercent(string) (float64, error) { return 100, nil }

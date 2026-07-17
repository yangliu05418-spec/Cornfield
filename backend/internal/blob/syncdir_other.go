//go:build !unix

package blob

func syncDirectory(string) error { return nil }

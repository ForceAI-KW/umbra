//go:build !darwin

package image

func cloneFile(rawBase, dst string) error { return copyFile(rawBase, dst) }

package inspectvalidate

import "os"

// linkNoReplace installs tmpPath at destPath using a hard link followed by
// removing the temp file, which fails with an "already exists" error if
// destPath is created concurrently between our existence check and this
// call (true no-replace semantics, unlike os.Rename which would silently
// replace an existing file on POSIX systems).
func linkNoReplace(tmpPath, destPath string) error {
	if err := os.Link(tmpPath, destPath); err != nil {
		return err
	}
	return os.Remove(tmpPath)
}

package claim

import "os"

// osReadFile is the package's default file reader. Tests may override the
// package-level readFile variable to inject faults.
func osReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

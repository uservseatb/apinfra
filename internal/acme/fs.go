package acme

import "os"

// fileWriter writes host files. App passes sys.FileWriter; acme does not import sys.
type fileWriter interface {
	Write(filePath, content string) (bool, error)
	WriteFile(filePath, content string, perm os.FileMode) (bool, error)
}

package ledger

import (
	"fmt"
	"io"
	"os"
)

func dirEmpty(dirPath string) (bool, error) {
	f, err := os.Open(dirPath)
	if err != nil {
		return false, fmt.Errorf("open [%s]: %w", dirPath, err)
	}
	defer f.Close()

	if _, err := f.Readdir(1); err == io.EOF {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("empty [%s]: %w", dirPath, err)
	} else {
		return false, nil
	}
}

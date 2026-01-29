package main

import "os"

type tempFileReadCloser struct {
	*os.File
	path string
}

func (t *tempFileReadCloser) Close() error {
	err := t.File.Close()
	rmErr := os.Remove(t.path)
	if err != nil {
		return err
	}
	if rmErr != nil && !os.IsNotExist(rmErr) {
		return rmErr
	}
	return nil
}

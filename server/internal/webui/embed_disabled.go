//go:build !embedui

package webui

import (
	"errors"
	"io/fs"
)

func Dist() (fs.FS, error) {
	return nil, errors.New("web UI not embedded (build with -tags embedui after running npm run build)")
}


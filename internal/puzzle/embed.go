package puzzle

import (
	"embed"
	"io/fs"
)

// Data 内嵌残局数据（data/*.json）。
//
//go:embed all:data
var Data embed.FS

// Embedded 返回内嵌残局库。
func Embedded() (*Store, error) {
	sub, err := fs.Sub(Data, "data")
	if err != nil {
		return nil, err
	}
	return NewStore(sub)
}

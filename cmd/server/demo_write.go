package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type demoWriteConfig struct {
	Origin     string `json:"origin"`
	Socket     string `json:"socket"`
	IngressUID uint32 `json:"ingress_uid"`
}

func readDemoWriteConfig(path string) (*demoWriteConfig, error) {
	if path == "" {
		return nil, nil
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("demo writer configuration requires a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open demo writer configuration")
	}
	defer file.Close()
	const limit = 8 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) > limit {
		return nil, errors.New("could not read demo writer configuration")
	}
	var config demoWriteConfig
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid demo writer configuration")
	}
	if config.IngressUID == 0 || config.IngressUID == uint32(os.Geteuid()) ||
		!filepath.IsAbs(config.Socket) || filepath.Clean(config.Socket) != config.Socket {
		return nil, errors.New("demo writer requires an absolute socket path and a dedicated non-root ingress UID")
	}
	return &config, nil
}

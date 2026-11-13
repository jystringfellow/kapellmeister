package main

import (
	"io"
	"os"

	"github.com/jystringfellow/kapellmeister/internal/cao"
	"github.com/jystringfellow/kapellmeister/internal/loop"
	"github.com/jystringfellow/kapellmeister/internal/trust"
)

func newClient(url string, s loop.Settings, out io.Writer) *cao.Client {
	client := cao.NewClient(url)
	if s.TrustFolder {
		folders := &trust.Folders{Enabled: true, Out: out, Err: os.Stderr}
		client.BeforeLaunch = folders.BeforeLaunch
	}
	return client
}

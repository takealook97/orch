// replaced by models_refresh.go
package main

import (
	"context"
	"errors"
	"io"
)

type modelsRefreshOptions struct {
	Repo   string
	DryRun bool
	Out    io.Writer
}

func refreshModels(ctx context.Context, opts modelsRefreshOptions) error {
	return errors.New("models refresh is not available in this build")
}

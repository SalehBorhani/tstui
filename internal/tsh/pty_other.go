//go:build !(linux || darwin)

package tsh

import "context"

type ptyProcess struct{ Process }

func startPTY(context.Context, string, []string, []string, PTYOptions) (*ptyProcess, error) {
	return nil, ErrPTYUnsupported
}

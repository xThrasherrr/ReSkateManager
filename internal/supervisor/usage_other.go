//go:build !windows && !linux

package supervisor

import "errors"

func usage(int) (Usage, error) { return Usage{}, errors.ErrUnsupported }

//go:build !windows && !linux

package host

import "errors"

func read() (Reading, error) { return Reading{}, errors.ErrUnsupported }

func diskSpace(string) (Space, error) { return Space{}, errors.ErrUnsupported }

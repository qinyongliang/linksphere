//go:build !android

package agent

import (
	"context"
	"errors"
)

func validateRouterRefresh(iface string) error {
	if iface != "" {
		return errors.New("IPv6 router refresh requires rooted Android")
	}
	return nil
}

func readIPv6Routes(context.Context) (string, error) {
	return "", errors.New("IPv6 router refresh requires rooted Android")
}

func solicitIPv6Router(string) error {
	return errors.New("IPv6 router refresh requires rooted Android")
}

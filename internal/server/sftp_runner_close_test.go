package server

import (
	"errors"
	gossh "golang.org/x/crypto/ssh"
	"testing"
)

type closeOrderChannel struct {
	gossh.Channel
	halfCloses, closes int
	closeErr           error
}

func (c *closeOrderChannel) CloseWrite() error { c.halfCloses++; return nil }
func (c *closeOrderChannel) Close() error      { c.closes++; return c.closeErr }

func TestTargetSFTPAbortDoesNotHalfCloseConcurrentWriter(t *testing.T) {
	failure := errors.New("channel close failure")
	channel := &closeOrderChannel{closeErr: failure}
	runner := &targetSFTPRunner{channel: channel, stdin: channel}
	if err := runner.close(); !errors.Is(err, failure) {
		t.Fatalf("close error lost: %v", err)
	}
	if channel.halfCloses != 0 || channel.closes != 1 {
		t.Fatalf("abort must full-close only: half=%d full=%d", channel.halfCloses, channel.closes)
	}
}

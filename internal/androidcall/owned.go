package call

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The opt-in owned path serializes physical cleanup, media exit and terminal history.
// Failure to confirm cleanup quarantines this manager; no automatic retry/reset exists.
func (manager *Manager) runOwned(ctx context.Context, call *Call, media MediaFunc, answerTimeout, maxCallDuration time.Duration) {
	attempted := false
	var once sync.Once
	var cleanupErr error
	cleanup := func() {
		if !attempted {
			return
		}
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, cleanupErr = manager.runCommand(cleanupCtx, "input", "keyevent", "KEYCODE_ENDCALL")
			if cleanupErr == nil {
				cleanupErr = cleanupCtx.Err()
			}
		})
	}
	state, operationErr := func() (State, error) {
		if err := ctx.Err(); err != nil {
			return StateCancelled, err
		}
		err := manager.checkVoiceService(ctx)
		if ctx.Err() != nil {
			return StateCancelled, ctx.Err()
		}
		if err != nil {
			return StateFailed, err
		}
		if err := ctx.Err(); err != nil {
			return StateCancelled, err
		}
		attempted = true
		output, err := manager.runCommand(ctx, "am", "start", "--user", "0", "-a", "android.intent.action.CALL", "-d", "tel:"+call.Number)
		if ctx.Err() != nil {
			return StateCancelled, ctx.Err()
		}
		if err != nil {
			return StateFailed, fmt.Errorf("dial failed: %w: %s", err, strings.TrimSpace(output))
		}
		manager.setState(call, StateWaitingForAnswer)
		manager.observeOwned(ctx, call)
		answerCtx, cancel := context.WithTimeout(ctx, answerTimeout)
		err = manager.waitForActive(answerCtx)
		answerError := answerCtx.Err()
		cancel()
		if err == nil && answerError != nil {
			err = answerError
		}
		if ctx.Err() != nil {
			return StateCancelled, ctx.Err()
		}
		if err != nil {
			return StateFailed, fmt.Errorf("answer wait failed: %w", err)
		}
		answered := time.Now().UTC()
		manager.mu.Lock()
		call.AnsweredAt = &answered
		manager.mu.Unlock()
		manager.setState(call, StatePlaying)
		manager.observeOwned(ctx, call)
		mediaCtx, cancelMedia := context.WithTimeout(ctx, maxCallDuration)
		err = runOwnedMedia(mediaCtx, media, cleanup)
		mediaErr := mediaCtx.Err()
		cancelMedia()
		if ctx.Err() != nil {
			return StateCancelled, ctx.Err()
		}
		if err == nil && mediaErr != nil {
			err = mediaErr
		}
		if err != nil {
			if errors.Is(mediaErr, context.DeadlineExceeded) {
				return StateFailed, errors.New("maximum call duration reached")
			}
			return StateFailed, fmt.Errorf("in-call playback failed: %w", err)
		}
		return StateCompleted, nil
	}()
	cleanup()
	if state == StateCompleted && ctx.Err() != nil {
		state = StateCancelled
		operationErr = ctx.Err()
	}
	if cleanupErr != nil {
		manager.mu.Lock()
		manager.blocked = true
		manager.mu.Unlock()
		state = StateFailed
		operationErr = errors.Join(operationErr, fmt.Errorf("device cleanup unconfirmed: %w", cleanupErr))
	}
	message := ""
	if operationErr != nil {
		message = operationErr.Error()
	}
	manager.finish(call, state, message)
	manager.observeOwned(ctx, call)
}

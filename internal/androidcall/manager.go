package call

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type State string

const (
	maxCachedCalls              = 100
	StateDialing          State = "dialing"
	StateWaitingForAnswer State = "waiting_for_answer"
	StatePlaying          State = "playing"
	StateCompleted        State = "completed"
	StateFailed           State = "failed"
	StateCancelled        State = "cancelled"
)

var (
	numberPattern     = regexp.MustCompile(`^\+?[0-9]{3,31}$`)
	activePattern     = regexp.MustCompile(`(?im)\b(?:state|mstate)\s*[:=]\s*ACTIVE\b`)
	callListPattern   = regexp.MustCompile(`(?m)^\s*\[[A-Z]+@\d+,\s*([A-Z_]+),`)
	voiceStatePattern = regexp.MustCompile(`\bmVoiceRegState=\d+\([A-Z_]+\)`)
	voiceReadyPattern = regexp.MustCompile(`\bmVoiceRegState=0\(IN_SERVICE\)`)
)

type Request struct {
	Number              string `json:"number"`
	AudioID             string `json:"audio_id"`
	AnswerTimeoutSecs   int    `json:"answer_timeout_seconds"`
	MaxCallSecs         int    `json:"max_call_seconds"`
	HangupAfterPlayback *bool  `json:"hangup_after_playback"`
}

type Call struct {
	ID         string     `json:"id"`
	Number     string     `json:"number"`
	AudioID    string     `json:"audio_id"`
	State      State      `json:"state"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`

	cancel context.CancelFunc
}

type Config struct {
	// CommandRunner is a trusted per-manager dependency, never request-controlled.
	// Nil preserves the original argument-separated OS command implementation.
	CommandRunner   func(context.Context, string, ...string) (string, error)
	Player          Player
	PollInterval    time.Duration
	Logger          *log.Logger
	AuthorizeNumber func(string) bool
	History         History
}

type Player interface {
	Play(context.Context, string) error
}

type MediaFunc func(context.Context) error

type History interface {
	Save(*Call) error
	Get(string) (*Call, bool, error)
	List(limit int, cursor string) ([]*Call, string, error)
}

type Manager struct {
	config  Config
	mu      sync.Mutex
	calls   map[string]*Call
	active  string
	blocked bool
}

func NewManager(config Config) *Manager {
	if config.PollInterval == 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	if config.Logger == nil {
		config.Logger = log.Default()
	}
	return &Manager{config: config, calls: make(map[string]*Call)}
}

func ValidNumber(number string) bool { return numberPattern.MatchString(number) }

func (manager *Manager) Start(request Request, audioPath string) (*Call, error) {
	if manager.config.Player == nil {
		return nil, errors.New("audio player is not configured")
	}
	return manager.StartMedia(request, request.AudioID, func(ctx context.Context) error {
		return manager.config.Player.Play(ctx, audioPath)
	})
}

func (manager *Manager) StartMedia(request Request, mediaID string, media MediaFunc) (*Call, error) {
	result, _, err := manager.startMediaOperation(context.Background(), request, mediaID, media, false)
	return result, err
}

// RunMediaContext owns a synchronous operation lifetime. It returns only after
// the worker and any started media callback have exited, including device cleanup.
// A non-cooperative driver or cleanup can keep this call and the manager busy.
// Context cancellation requests shutdown; it is not proof of physical completion.
func (manager *Manager) RunMediaContext(ctx context.Context, request Request, mediaID string, media MediaFunc) (*Call, error) {
	_, done, err := manager.startMediaOperation(ctx, request, mediaID, media, true)
	if err != nil {
		return nil, err
	}
	result := <-done
	if result.State != StateCompleted {
		if result.State == StateCancelled && ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("owned call did not complete successfully")
	}
	return result, nil
}

func (manager *Manager) startMediaOperation(parent context.Context, request Request, mediaID string, media MediaFunc, owned bool) (*Call, <-chan *Call, error) {
	if parent == nil {
		return nil, nil, errors.New("call context is required")
	}
	if err := parent.Err(); err != nil {
		return nil, nil, err
	}
	if owned && request.HangupAfterPlayback != nil && !*request.HangupAfterPlayback {
		return nil, nil, errors.New("owned call requires hangup after playback")
	}
	if !ValidNumber(request.Number) {
		return nil, nil, errors.New("number must contain an optional leading + and 3 to 31 digits")
	}
	if manager.config.AuthorizeNumber != nil && !manager.config.AuthorizeNumber(request.Number) {
		return nil, nil, errors.New("number is not allowed by phone policy")
	}
	if media == nil {
		return nil, nil, errors.New("media handler is required")
	}
	if request.AnswerTimeoutSecs == 0 {
		request.AnswerTimeoutSecs = 45
	}
	if request.AnswerTimeoutSecs < 5 || request.AnswerTimeoutSecs > 180 {
		return nil, nil, errors.New("answer_timeout_seconds must be between 5 and 180")
	}
	if request.MaxCallSecs == 0 {
		request.MaxCallSecs = 300
	}
	if request.MaxCallSecs < 5 || request.MaxCallSecs > 1800 {
		return nil, nil, errors.New("max_call_seconds must be between 5 and 1800")
	}

	manager.mu.Lock()
	if err := parent.Err(); err != nil {
		manager.mu.Unlock()
		return nil, nil, err
	}
	if manager.active != "" {
		manager.mu.Unlock()
		return nil, nil, errors.New("another call is active")
	}
	manager.pruneCache()
	id, err := randomID()
	if err != nil {
		manager.mu.Unlock()
		return nil, nil, err
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(parent)
	call := &Call{
		ID: id, Number: request.Number, AudioID: mediaID,
		State: StateDialing, CreatedAt: now, UpdatedAt: now, cancel: cancel,
	}
	manager.calls[id] = call
	manager.active = id
	result := clone(call)
	manager.mu.Unlock()
	manager.persist(result)
	if owned {
		manager.observeOwned(ctx, call)
	}
	hangup := request.HangupAfterPlayback == nil || *request.HangupAfterPlayback
	done := make(chan *Call, 1)
	go func() {
		defer func() {
			cancel()
			manager.mu.Lock()
			final := clone(call)
			manager.mu.Unlock()
			done <- final
			close(done)
		}()
		manager.run(ctx, call, media, time.Duration(request.AnswerTimeoutSecs)*time.Second, time.Duration(request.MaxCallSecs)*time.Second, hangup, owned)
	}()
	return result, done, nil
}

func (manager *Manager) pruneCache() {
	if len(manager.calls) < maxCachedCalls {
		return
	}
	var oldest *Call
	for _, item := range manager.calls {
		if item.ID == manager.active {
			continue
		}
		if oldest == nil || item.CreatedAt.Before(oldest.CreatedAt) {
			oldest = item
		}
	}
	if oldest != nil {
		delete(manager.calls, oldest.ID)
	}
}

func (manager *Manager) Get(id string) (*Call, bool) {
	manager.mu.Lock()
	call, found := manager.calls[id]
	if found {
		result := clone(call)
		manager.mu.Unlock()
		return result, true
	}
	manager.mu.Unlock()
	if manager.config.History == nil {
		return nil, false
	}
	result, found, err := manager.config.History.Get(id)
	if err != nil {
		manager.config.Logger.Printf("read call %s from history: %v", id, err)
		return nil, false
	}
	return result, found
}

func (manager *Manager) List() []*Call {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	result := make([]*Call, 0, len(manager.calls))
	for _, item := range manager.calls {
		result = append(result, clone(item))
	}
	slices.SortFunc(result, func(left, right *Call) int {
		return right.CreatedAt.Compare(left.CreatedAt)
	})
	return result
}

func (manager *Manager) ListHistory(limit int, cursor string) ([]*Call, string, error) {
	if manager.config.History != nil {
		return manager.config.History.List(limit, cursor)
	}
	items := manager.List()
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, "", nil
}

func (manager *Manager) Active() bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return manager.active != ""
}

// Cancel requests cancellation; only the worker owns device cleanup and terminal history.
// The returned snapshot may still be nonterminal while cleanup is in progress.
func (manager *Manager) Cancel(id string) (*Call, bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	call, found := manager.calls[id]
	if !found {
		return nil, false
	}
	if call.cancel != nil {
		call.cancel()
	}
	return clone(call), true
}

func (manager *Manager) run(ctx context.Context, call *Call, media MediaFunc, answerTimeout, maxCallDuration time.Duration, hangup bool, owned bool) {
	defer func() {
		manager.mu.Lock()
		if manager.active == call.ID && !manager.blocked {
			manager.active = ""
		}
		manager.mu.Unlock()
	}()
	if owned {
		manager.runOwned(ctx, call, media, answerTimeout, maxCallDuration)
		return
	}
	if err := manager.checkVoiceService(ctx); err != nil {
		manager.fail(call, err)
		return
	}
	manager.config.Logger.Printf("call %s dialing %s", call.ID, call.Number)
	if output, err := manager.runCommand(ctx, "am", "start", "--user", "0", "-a", "android.intent.action.CALL", "-d", "tel:"+call.Number); err != nil {
		manager.fail(call, fmt.Errorf("dial failed: %w: %s", err, strings.TrimSpace(output)))
		return
	}
	manager.setState(call, StateWaitingForAnswer)

	answerCtx, cancel := context.WithTimeout(ctx, answerTimeout)
	err := manager.waitForActive(answerCtx)
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			manager.finish(call, StateCancelled, "")
		} else {
			manager.fail(call, fmt.Errorf("answer wait failed: %w", err))
		}
		manager.hangup()
		return
	}
	answered := time.Now().UTC()
	manager.mu.Lock()
	call.AnsweredAt = &answered
	manager.mu.Unlock()
	manager.setState(call, StatePlaying)

	mediaCtx, cancelMedia := context.WithTimeout(ctx, maxCallDuration)
	err = runMedia(mediaCtx, media)
	mediaContextError := mediaCtx.Err()
	cancelMedia()
	if errors.Is(ctx.Err(), context.Canceled) {
		manager.finish(call, StateCancelled, "")
		manager.hangup()
		return
	}
	if err != nil {
		if errors.Is(mediaContextError, context.DeadlineExceeded) {
			manager.fail(call, errors.New("maximum call duration reached"))
		} else {
			manager.fail(call, fmt.Errorf("in-call playback failed: %w", err))
		}
		manager.hangup()
		return
	}
	if hangup {
		manager.hangup()
	}
	manager.finish(call, StateCompleted, "")
}

func runMedia(ctx context.Context, media MediaFunc) error {
	result := make(chan error, 1)
	go func() { result <- media(ctx) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) checkVoiceService(ctx context.Context) error {
	output, err := manager.runCommand(ctx, "dumpsys", "telephony.registry")
	if err != nil {
		// Some ROMs restrict this dump. Let Telecom make the final decision there.
		return nil
	}
	if voiceReadyPattern.MatchString(output) {
		return nil
	}
	if voiceStatePattern.MatchString(output) {
		return errors.New("cellular voice service is unavailable; check SIM and network registration")
	}
	return nil
}

func (manager *Manager) waitForActive(ctx context.Context) error {
	ticker := time.NewTicker(manager.config.PollInterval)
	defer ticker.Stop()
	seenCall := false
	missingPolls := 0
	for {
		output, err := manager.runCommand(ctx, "dumpsys", "telecom")
		if err == nil {
			state, found := liveTelecomState(output)
			if found {
				seenCall = true
				missingPolls = 0
				if state == "ACTIVE" {
					return nil
				}
				if state == "DISCONNECTED" || state == "ABORTED" {
					return errors.New("cellular call ended before answer")
				}
			} else if seenCall {
				missingPolls++
				if missingPolls >= 2 {
					return errors.New("cellular call ended before answer")
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func liveTelecomState(output string) (string, bool) {
	section := output
	if index := strings.Index(section, "\nAnalytics:"); index >= 0 {
		section = section[:index]
	}
	if match := callListPattern.FindStringSubmatch(section); len(match) == 2 {
		return match[1], true
	}
	if activePattern.MatchString(section) {
		return "ACTIVE", true
	}
	return "", false
}

func (manager *Manager) hangup() {
	_, _ = manager.runCommand(context.Background(), "input", "keyevent", "KEYCODE_ENDCALL")
}

func (manager *Manager) setState(call *Call, state State) {
	manager.mu.Lock()
	if isTerminal(call.State) {
		manager.mu.Unlock()
		return
	}
	call.State = state
	call.UpdatedAt = time.Now().UTC()
	result := clone(call)
	manager.mu.Unlock()
	manager.persist(result)
}

func (manager *Manager) fail(call *Call, err error) {
	manager.config.Logger.Printf("call %s failed: %v", call.ID, err)
	manager.finish(call, StateFailed, err.Error())
}

func (manager *Manager) finish(call *Call, state State, message string) {
	manager.mu.Lock()
	if call.State == StateCancelled && state != StateCancelled {
		manager.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	call.State = state
	call.Error = message
	call.UpdatedAt = now
	call.EndedAt = &now
	call.cancel = nil
	result := clone(call)
	manager.mu.Unlock()
	manager.persist(result)
}

func (manager *Manager) persist(item *Call) {
	if manager.config.History == nil {
		return
	}
	if err := manager.config.History.Save(item); err != nil {
		manager.config.Logger.Printf("persist call %s: %v", item.ID, err)
	}
}

func isTerminal(state State) bool {
	return state == StateCompleted || state == StateFailed || state == StateCancelled
}

func runCommand(ctx context.Context, name string, arguments ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	return string(output), err
}

func randomID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func clone(source *Call) *Call {
	copy := *source
	copy.cancel = nil
	return &copy
}

// runCommand isolates device execution for adapters and deterministic tests.
func (manager *Manager) runCommand(ctx context.Context, name string, arguments ...string) (string, error) {
	if manager.config.CommandRunner != nil {
		return manager.config.CommandRunner(ctx, name, arguments...)
	}
	return runCommand(ctx, name, arguments...)
}

// The owned path never releases the device slot while the driver is still alive.
func runOwnedMedia(ctx context.Context, media MediaFunc, cleanup func()) error {
	if err := ctx.Err(); err != nil {
		cleanup()
		return err
	}
	result := make(chan error, 1)
	go func() {
		if err := ctx.Err(); err != nil {
			result <- err
			return
		}
		result <- media(ctx)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		cleanup()
		<-result
		return ctx.Err()
	}
}

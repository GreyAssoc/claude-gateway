package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// Compile-time check: *Switcher implements Backend.
var _ Backend = (*Switcher)(nil)

// Switcher wraps multiple Backend implementations and delegates to the active one.
// Allows runtime switching between frameworks (e.g. Claude Code ↔ OpenCode)
// without restarting the gateway.
type Switcher struct {
	mu       sync.RWMutex
	active   string
	backends map[string]Backend
}

// NewSwitcher creates a framework switcher. The initial backend is set to defaultName.
// All provided backends must have unique Name() values.
func NewSwitcher(defaultName string, backends ...Backend) (*Switcher, error) {
	s := &Switcher{
		backends: make(map[string]Backend, len(backends)),
	}

	for _, b := range backends {
		name := b.Name()
		if _, exists := s.backends[name]; exists {
			return nil, fmt.Errorf("duplicate backend name: %s", name)
		}
		s.backends[name] = b
	}

	if _, ok := s.backends[defaultName]; !ok {
		return nil, fmt.Errorf("default backend %q not registered", defaultName)
	}

	s.active = defaultName
	log.Printf("Framework switcher: active=%s, available=%v", defaultName, s.Available())
	return s, nil
}

// Switch changes the active backend at runtime. Returns an error if the
// requested backend is not registered.
func (s *Switcher) Switch(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.backends[name]; !ok {
		return fmt.Errorf("unknown backend: %s (available: %v)", name, s.availableLocked())
	}

	old := s.active
	s.active = name
	log.Printf("Framework switched: %s → %s", old, name)
	return nil
}

// Active returns the name of the currently active backend.
func (s *Switcher) Active() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// Available returns the names of all registered backends.
func (s *Switcher) Available() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.availableLocked()
}

func (s *Switcher) availableLocked() []string {
	names := make([]string, 0, len(s.backends))
	for name := range s.backends {
		names = append(names, name)
	}
	return names
}

// get returns the currently active backend.
func (s *Switcher) get() Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backends[s.active]
}

// --- Backend interface delegation ---

func (s *Switcher) Ask(ctx context.Context, sessionID, prompt, workDir string, maxTurns int) (*Response, error) {
	return s.get().Ask(ctx, sessionID, prompt, workDir, maxTurns)
}

func (s *Switcher) AskWithOptions(ctx context.Context, opts AskOptions) (*Response, error) {
	return s.get().AskWithOptions(ctx, opts)
}

func (s *Switcher) AskStreaming(ctx context.Context, opts AskOptions, onProgress func(StreamEvent)) (*Response, error) {
	return s.get().AskStreaming(ctx, opts, onProgress)
}

func (s *Switcher) RunIsolatedAgent(ctx context.Context, opts RunAgentOptions) (*Response, error) {
	return s.get().RunIsolatedAgent(ctx, opts)
}

func (s *Switcher) AbortSession(sessionID string) {
	s.get().AbortSession(sessionID)
}

func (s *Switcher) ResetSession(ctx context.Context, sessionID string) error {
	return s.get().ResetSession(ctx, sessionID)
}

func (s *Switcher) CheckSession(ctx context.Context, sessionID string) (*SessionStatus, error) {
	return s.get().CheckSession(ctx, sessionID)
}

func (s *Switcher) Health(ctx context.Context) error {
	return s.get().Health(ctx)
}

func (s *Switcher) IsHealthy() bool {
	return s.get().IsHealthy()
}

func (s *Switcher) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var firstErr error
	for _, b := range s.backends {
		if err := b.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Switcher) Name() string {
	return s.get().Name()
}

// GetOpenCodeClient returns the OpenCode backend if registered, or nil.
func (s *Switcher) GetOpenCodeClient() *OpenCodeClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if b, ok := s.backends["opencode"]; ok {
		if oc, ok := b.(*OpenCodeClient); ok {
			return oc
		}
	}
	return nil
}

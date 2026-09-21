package cursor

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	sdkv1 "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/sdk/v1"
)

const (
	sessionLimit = 64
	sessionTTL   = 30 * time.Minute
)

type sessionEntry struct {
	key         string
	fingerprint string
	process     *bridgeProcess
	agentID     string
	apiKey      string
	inUse       bool
	lastUsed    time.Time
}

// sessionStore caches live agents keyed by conversation fingerprint.
type sessionStore struct {
	mu    sync.Mutex
	byKey map[string]*sessionEntry
	order []*sessionEntry
}

func (s *sessionStore) initLocked() {
	if s.byKey == nil {
		s.byKey = make(map[string]*sessionEntry)
	}
}

func sessionCacheKey(req generateRequest, prefix string) string {
	sum := sha256.Sum256([]byte(
		tenantFingerprint(req.apiKey, req.proxyURL) + "\n" +
			req.model + "\n" +
			modelParamSignature(req.params) + "\n" +
			toolSetSignature(req.tools) + "\n" +
			prefix,
	))
	return hex.EncodeToString(sum[:])
}

func modelParamSignature(params []modelParam) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, param := range params {
		parts = append(parts, param.ID+"="+param.Value)
	}
	return strings.Join(parts, ",")
}

func toolSetSignature(tools map[string]*sdkv1.CustomToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// occupySession looks up an agent whose prior transcript matches the request prefix. A hit
// marks the entry in-use so a concurrent request with the same key falls back to a one-shot
// agent rather than sharing a run. ok is false on any uncertainty.
func (r *Runtime) occupySession(req generateRequest) (*sessionEntry, string, []chatImage, bool) {
	if len(req.tools) > 0 || len(req.toolResults) > 0 || req.sessionPrefix == "" || strings.TrimSpace(req.sessionIncrement) == "" {
		return nil, "", nil, false
	}
	key := sessionCacheKey(req, req.sessionPrefix)
	fingerprint := tenantFingerprint(req.apiKey, req.proxyURL)

	r.sessions.mu.Lock()
	r.sessions.initLocked()
	defer r.sessions.mu.Unlock()
	entry := r.sessions.byKey[key]
	if entry == nil || entry.inUse || entry.fingerprint != fingerprint {
		return nil, "", nil, false
	}
	if entry.process == nil || !entry.process.alive() {
		r.removeSessionLocked(entry)
		return nil, "", nil, false
	}
	if time.Since(entry.lastUsed) > sessionTTL {
		r.removeSessionLocked(entry)
		go releaseAgent(entry.process, entry.agentID, entry.apiKey)
		return nil, "", nil, false
	}
	entry.inUse = true
	entry.lastUsed = time.Now()
	r.touchSessionLocked(entry)
	return entry, req.sessionIncrement, req.incrementImages, true
}

func (r *Runtime) rememberSession(req generateRequest, process *bridgeProcess, agentID, assistantText string) bool {
	if len(req.tools) > 0 || len(req.toolResults) > 0 || process == nil || agentID == "" {
		return false
	}
	prefix := nextSessionPrefix(req, assistantText)
	if prefix == "" {
		return false
	}
	entry := &sessionEntry{
		key:         sessionCacheKey(req, prefix),
		fingerprint: tenantFingerprint(req.apiKey, req.proxyURL),
		process:     process,
		agentID:     agentID,
		apiKey:      req.apiKey,
		lastUsed:    time.Now(),
	}
	r.sessions.mu.Lock()
	r.sessions.initLocked()
	defer r.sessions.mu.Unlock()
	if existing := r.sessions.byKey[entry.key]; existing != nil {
		if existing.inUse {
			// A concurrent one-shot must not steal the agent that is still running this key.
			return false
		}
		r.removeSessionLocked(existing)
		go releaseAgent(existing.process, existing.agentID, existing.apiKey)
	}
	r.sessions.byKey[entry.key] = entry
	r.sessions.order = append(r.sessions.order, entry)
	r.evictOverflowLocked()
	return true
}

func (r *Runtime) rekeySession(entry *sessionEntry, req generateRequest, assistantText string) {
	if entry == nil {
		return
	}
	prefix := nextSessionPrefix(req, assistantText)
	newKey := sessionCacheKey(req, prefix)
	r.sessions.mu.Lock()
	r.sessions.initLocked()
	defer r.sessions.mu.Unlock()
	if r.sessions.byKey[entry.key] == entry {
		delete(r.sessions.byKey, entry.key)
	}
	if existing := r.sessions.byKey[newKey]; existing != nil && existing != entry {
		r.removeSessionLocked(existing)
		go releaseAgent(existing.process, existing.agentID, existing.apiKey)
	}
	entry.key = newKey
	entry.inUse = false
	entry.lastUsed = time.Now()
	r.sessions.byKey[newKey] = entry
	r.touchSessionLocked(entry)
}

func (r *Runtime) releaseSession(entry *sessionEntry, keep bool) {
	if entry == nil {
		return
	}
	r.sessions.mu.Lock()
	r.sessions.initLocked()
	defer r.sessions.mu.Unlock()
	if !keep {
		if r.sessions.byKey[entry.key] == entry {
			r.removeSessionLocked(entry)
			go releaseAgent(entry.process, entry.agentID, entry.apiKey)
		}
		return
	}
	entry.inUse = false
	entry.lastUsed = time.Now()
	r.touchSessionLocked(entry)
}

func nextSessionPrefix(req generateRequest, assistantText string) string {
	history := strings.TrimSpace(req.prompt)
	if req.sessionPrefix != "" {
		history = strings.TrimSpace(req.sessionPrefix)
		if req.sessionIncrement != "" {
			history = strings.TrimSpace(history + "\n\n" + req.sessionIncrement)
		}
	} else {
		history = labeledUserHistory(req.prompt)
	}
	if strings.TrimSpace(assistantText) == "" {
		return history
	}
	return strings.TrimSpace(history + "\n\nAssistant: " + assistantText)
}

func labeledUserHistory(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if strings.Contains(prompt, "User: ") || strings.Contains(prompt, "Assistant: ") || strings.Contains(prompt, "Tool result") {
		return prompt
	}
	return "User: " + prompt
}

func (r *Runtime) evictAllSessions() {
	r.sessions.mu.Lock()
	entries := r.sessions.order
	r.sessions.byKey = make(map[string]*sessionEntry)
	r.sessions.order = nil
	r.sessions.mu.Unlock()
	r.releaseSessionAgents(entries)
}

func (r *Runtime) evictSessionsForProcess(p *bridgeProcess) {
	if p == nil {
		return
	}
	r.sessions.mu.Lock()
	var victims []*sessionEntry
	for _, entry := range r.sessions.order {
		if entry.process == p {
			victims = append(victims, entry)
		}
	}
	for _, entry := range victims {
		r.removeSessionLocked(entry)
	}
	r.sessions.mu.Unlock()
	r.releaseSessionAgents(victims)
}

// releaseSessionAgents tears the agents down in parallel. Serially they would take up to
// agentCleanupTimeout each, and quiesce and reconfigure both call this on a deadline the host
// controls; a cache at its limit would blow through it.
func (r *Runtime) releaseSessionAgents(entries []*sessionEntry) {
	var wg sync.WaitGroup
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		wg.Add(1)
		go func(entry *sessionEntry) {
			defer wg.Done()
			releaseAgent(entry.process, entry.agentID, entry.apiKey)
		}(entry)
	}
	wg.Wait()
}

func (r *Runtime) removeSessionLocked(entry *sessionEntry) {
	if entry == nil {
		return
	}
	if r.sessions.byKey[entry.key] == entry {
		delete(r.sessions.byKey, entry.key)
	}
	for i, candidate := range r.sessions.order {
		if candidate == entry {
			r.sessions.order = append(r.sessions.order[:i], r.sessions.order[i+1:]...)
			break
		}
	}
}

func (r *Runtime) touchSessionLocked(entry *sessionEntry) {
	for i, candidate := range r.sessions.order {
		if candidate == entry {
			r.sessions.order = append(r.sessions.order[:i], r.sessions.order[i+1:]...)
			r.sessions.order = append(r.sessions.order, entry)
			return
		}
	}
	r.sessions.order = append(r.sessions.order, entry)
}

func (r *Runtime) evictOverflowLocked() {
	for len(r.sessions.order) > sessionLimit {
		oldest := r.sessions.order[0]
		r.removeSessionLocked(oldest)
		go releaseAgent(oldest.process, oldest.agentID, oldest.apiKey)
	}
}

func (r *Runtime) sessionCount() int {
	r.sessions.mu.Lock()
	r.sessions.initLocked()
	defer r.sessions.mu.Unlock()
	return len(r.sessions.byKey)
}

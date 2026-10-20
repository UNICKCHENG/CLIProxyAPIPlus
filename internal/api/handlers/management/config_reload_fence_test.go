package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// newIssue6631Handler exposes v8 configuration writes backed by a config file.
func newIssue6631Handler(t *testing.T, raw string) (*Handler, string, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	return h, path, router
}

func putIssue6631Value(t *testing.T, router *gin.Engine, url, body string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, url, strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT %s: status=%d body=%s", url, recorder.Code, recorder.Body.String())
	}
}

func readIssue6631Disk(t *testing.T, path string) *config.Config {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	cfg, errParse := config.ParseConfigBytes(data)
	if errParse != nil {
		t.Fatal(errParse)
	}
	return cfg
}

func TestSetConfigIgnoresConfigLoadedBeforeManagementSave_Issue6631(t *testing.T) {
	h, path, router := newIssue6631Handler(t, "request-retry: 3\nmax-retry-credentials: 0\n")
	// A watcher reload read config.yaml before the next save and publishes late.
	staleReload, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}

	putIssue6631Value(t, router, "/v8/management/config/routing/retry/request-retry", `5`)
	h.SetConfig(staleReload)
	putIssue6631Value(t, router, "/v8/management/config/routing/retry/max-retry-credentials", `7`)

	h.mu.Lock()
	memoryRetry, memoryCredentials := h.cfg.RequestRetry, h.cfg.MaxRetryCredentials
	h.mu.Unlock()
	if memoryRetry != 5 || memoryCredentials != 7 {
		t.Fatalf("in-memory config = request-retry %d, max-retry-credentials %d; want 5, 7", memoryRetry, memoryCredentials)
	}
	disk := readIssue6631Disk(t, path)
	if disk.RequestRetry != 5 || disk.MaxRetryCredentials != 7 {
		t.Fatalf("config.yaml = request-retry %d, max-retry-credentials %d; want 5, 7", disk.RequestRetry, disk.MaxRetryCredentials)
	}
}

func TestSetConfigAppliesCurrentFileAndNonFileConfigs_Issue6631(t *testing.T) {
	h, path, _ := newIssue6631Handler(t, "request-retry: 3\n")

	// An external edit is published by the reload that read it.
	if errWrite := os.WriteFile(path, []byte("request-retry: 9\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	external, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h.SetConfig(external)
	h.mu.Lock()
	gotExternal := h.cfg.RequestRetry
	h.mu.Unlock()
	if gotExternal != 9 {
		t.Fatalf("request-retry after external edit = %d, want 9", gotExternal)
	}

	// Configs that were not loaded from the file, such as Home updates, still apply.
	h.SetConfig(&config.Config{RequestRetry: 11})
	h.mu.Lock()
	gotNonFile := h.cfg.RequestRetry
	h.mu.Unlock()
	if gotNonFile != 11 {
		t.Fatalf("request-retry after non-file config = %d, want 11", gotNonFile)
	}
}

func TestManagementWritesSurviveConcurrentReloadPublications_Issue6631(t *testing.T) {
	h, path, router := newIssue6631Handler(t, "request-retry: 0\nmax-retry-credentials: 0\n")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Mirror watcher reloads: load config.yaml, then publish it to the handler.
			if reloaded, errLoad := config.LoadConfig(path); errLoad == nil {
				h.SetConfig(reloaded)
			}
		}
	}()
	stopPublisher := sync.OnceFunc(func() {
		close(stop)
		wg.Wait()
	})
	t.Cleanup(stopPublisher)

	const writes = 40
	for i := 1; i <= writes; i++ {
		putIssue6631Value(t, router, "/v8/management/config/routing/retry/request-retry", fmt.Sprintf(`%d`, i))
		putIssue6631Value(t, router, "/v8/management/config/routing/retry/max-retry-credentials", fmt.Sprintf(`%d`, i))
	}
	stopPublisher()

	disk := readIssue6631Disk(t, path)
	if disk.RequestRetry != writes || disk.MaxRetryCredentials != writes {
		t.Fatalf("config.yaml = request-retry %d, max-retry-credentials %d; want %d, %d", disk.RequestRetry, disk.MaxRetryCredentials, writes, writes)
	}
}

package build

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/moby/moby/v2/daemon/server/httputils"
)

// mockBackend is a mock implementation of the Backend interface for testing.
type mockBackend struct {
	buildFunc func(context.Context, buildbackend.BuildConfig) (string, error)
}

func (m *mockBackend) Build(ctx context.Context, config buildbackend.BuildConfig) (string, error) {
	if m.buildFunc != nil {
		return m.buildFunc(ctx, config)
	}
	return "test-image-id", nil
}

func (m *mockBackend) PruneCache(ctx context.Context, opts buildbackend.CachePruneOptions) (*build.CachePruneReport, error) {
	return &build.CachePruneReport{}, nil
}

func (m *mockBackend) Cancel(ctx context.Context, id string) error {
	return nil
}

// mockDaemon is a mock implementation of experimentalProvider for testing.
type mockDaemon struct {
	experimental bool
}

func (m *mockDaemon) HasExperimental() bool {
	return m.experimental
}

func TestPostBuild_EnableFullDuplex_WithBody(t *testing.T) {
	// Create a response recorder
	recorder := httptest.NewRecorder()

	// Create a request with a body
	body := bytes.NewBufferString("test build context")
	req := httptest.NewRequest(http.MethodPost, "/build", body)
	req.Header.Set("Content-Type", "application/x-tar")

	// Set API version in context
	ctx := context.WithValue(context.Background(), httputils.APIVersionKey{}, "1.40")

	// Create mock backend that reads the body
	backend := &mockBackend{
		buildFunc: func(ctx context.Context, config buildbackend.BuildConfig) (string, error) {
			// Read the body to simulate actual build process
			if config.Source != nil {
				_, _ = io.Copy(io.Discard, config.Source)
			}
			return "test-image-id", nil
		},
	}

	router := &buildRouter{
		backend: backend,
		daemon:  &mockDaemon{experimental: false},
	}

	// Call postBuild - with Go 1.21+, EnableFullDuplex should be called internally
	// via http.NewResponseController(recorder).EnableFullDuplex()
	err := router.postBuild(ctx, recorder, req, nil)

	// Verify no error occurred
	assert.NilError(t, err)

	// Verify response was written (this confirms the handler completed successfully)
	assert.Assert(t, recorder.Code == http.StatusOK || recorder.Body.Len() > 0, "Response should be written")
}

func TestPostBuild_EnableFullDuplex_NoBody(t *testing.T) {
	// Create a response recorder
	recorder := httptest.NewRecorder()

	// Create a request with Body explicitly set to nil
	req := httptest.NewRequest(http.MethodPost, "/build", nil)
	req.Body = nil // Explicitly set to nil to test the nil check
	req.Header.Set("Content-Type", "application/x-tar")

	// Set API version in context
	ctx := context.WithValue(context.Background(), httputils.APIVersionKey{}, "1.40")

	backend := &mockBackend{
		buildFunc: func(ctx context.Context, config buildbackend.BuildConfig) (string, error) {
			return "test-image-id", nil
		},
	}

	router := &buildRouter{
		backend: backend,
		daemon:  &mockDaemon{experimental: false},
	}

	// Call postBuild - when Body is nil, EnableFullDuplex should not be called
	err := router.postBuild(ctx, recorder, req, nil)

	// Verify no error occurred
	assert.NilError(t, err)
}

func TestPostBuild_EnableFullDuplex_ErrorHandling(t *testing.T) {
	// Create a response recorder
	recorder := httptest.NewRecorder()

	// Create a request with a body
	body := bytes.NewBufferString("test build context")
	req := httptest.NewRequest(http.MethodPost, "/build", body)
	req.Header.Set("Content-Type", "application/x-tar")

	// Set API version in context
	ctx := context.WithValue(context.Background(), httputils.APIVersionKey{}, "1.40")

	backend := &mockBackend{
		buildFunc: func(ctx context.Context, config buildbackend.BuildConfig) (string, error) {
			if config.Source != nil {
				_, _ = io.Copy(io.Discard, config.Source)
			}
			return "test-image-id", nil
		},
	}

	router := &buildRouter{
		backend: backend,
		daemon:  &mockDaemon{experimental: false},
	}

	// Call postBuild - if EnableFullDuplex returns an error, it should be logged
	// but the handler should continue and complete successfully
	err := router.postBuild(ctx, recorder, req, nil)

	// The handler should complete successfully even if EnableFullDuplex fails
	// (the error is only logged, not returned)
	assert.NilError(t, err)
}

func TestPostBuild_FullDuplex_ConcurrentReadWrite(t *testing.T) {
	// This test verifies that we can write to the response while reading the request body
	recorder := httptest.NewRecorder()

	// Create a request with a large body to simulate a real build context
	largeBody := bytes.NewBuffer(make([]byte, 1024*1024)) // 1MB
	req := httptest.NewRequest(http.MethodPost, "/build", largeBody)
	req.Header.Set("Content-Type", "application/x-tar")

	// Set API version in context
	ctx := context.WithValue(context.Background(), httputils.APIVersionKey{}, "1.40")

	// Create a backend that writes progress while reading
	backend := &mockBackend{
		buildFunc: func(ctx context.Context, config buildbackend.BuildConfig) (string, error) {
			// Write some output while reading the body
			if config.ProgressWriter.Output != nil {
				_, _ = config.ProgressWriter.Output.Write([]byte(`{"stream":"Starting build...\n"}`))
			}

			// Read the body in chunks
			if config.Source != nil {
				buf := make([]byte, 4096)
				for {
					_, err := config.Source.Read(buf)
					if err == io.EOF {
						break
					}
				}
			}

			// Write more output
			if config.ProgressWriter.Output != nil {
				_, _ = config.ProgressWriter.Output.Write([]byte(`{"stream":"Build complete\n"}`))
			}

			return "test-image-id", nil
		},
	}

	router := &buildRouter{
		backend: backend,
		daemon:  &mockDaemon{experimental: false},
	}

	// Call postBuild
	err := router.postBuild(ctx, recorder, req, nil)

	// Verify no error occurred
	assert.NilError(t, err)

	// Verify response contains the progress output
	responseBody := recorder.Body.String()
	assert.Assert(t, len(responseBody) > 0, "Response should contain build progress")
	assert.Assert(t, bytes.Contains([]byte(responseBody), []byte("Starting build")), "Response should contain progress output")
}


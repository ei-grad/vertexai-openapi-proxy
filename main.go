package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
)

var logger *slog.Logger

func initSlogLogger() {
	var logLevel slog.Level
	logLevelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	switch logLevelStr {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo // Default
		if logLevelStr != "" && logLevelStr != "info" {
			// Use standard log here as slog isn't fully set up
			log.Printf("Warning: Invalid LOG_LEVEL '%s', defaulting to 'info'. Valid levels: debug, info, warn, error.", logLevelStr)
		}
	}

	var handler slog.Handler
	logFormatStr := strings.ToLower(os.Getenv("LOG_FORMAT"))
	opts := &slog.HandlerOptions{
		Level: logLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Customize time format if needed, or other attributes
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format(time.RFC3339Nano))
			}
			return a
		},
	}

	if logFormatStr == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts) // Default
		 if logFormatStr != "" && logFormatStr != "text" {
			log.Printf("Warning: Invalid LOG_FORMAT '%s', defaulting to 'text'. Valid formats: text, json.", logFormatStr)
		}
	}

	logger = slog.New(handler)

	// Redirect standard log package to slog
	// All standard log calls will go to slog at Info level
	slogBridge := slog.NewLogLogger(handler, slog.LevelInfo)
	log.SetOutput(slogBridge.Writer())
	log.SetFlags(0) // Disable standard log prefixes as slog handles formatting
}

// googleFindDefaultCredentialsWrapper wraps google.FindDefaultCredentials to allow mocking in tests.
var googleFindDefaultCredentials = google.FindDefaultCredentials

var (
	projectID string
	location  string
	// vertexAIAPIHostFormat specifies the format for the Vertex AI API host.
	// It's a variable to allow overriding for testing.
	// Example: "%s-aiplatform.googleapis.com" where %s is the location.
	vertexAIAPIHostFormat = "%s-aiplatform.googleapis.com"
)

// Model structure for /v1/models response (OpenAI compatible)
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelList structure for /v1/models response
type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

var (
	token      string
	tokenMutex sync.RWMutex
	expiry     time.Time
)

func getToken(ctx context.Context) (string, error) {
	tokenMutex.RLock()
	if time.Now().Before(expiry.Add(-time.Minute)) { // cached token still valid
		logger.Debug("getToken: Using cached token.")
		defer tokenMutex.RUnlock()
		return token, nil
	}
	tokenMutex.RUnlock()

	tokenMutex.Lock()
	defer tokenMutex.Unlock()
	logger.Info("getToken: Cache expired or empty, fetching new token.")

	// Use the wrapper variable so it can be mocked in tests
	creds, err := googleFindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		logger.Error("getToken: Error finding default credentials", "error", err)
		return "", err
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		logger.Error("getToken: Error getting token from source", "error", err)
		return "", err
	}
	token = tok.AccessToken
	expiry = tok.Expiry
	logger.Info("getToken: Successfully fetched new token.")
	return token, nil
}

func makeProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Log basic request info. Avoid logging full headers here to prevent excessive log volume.
			// Specific headers like Authorization are logged when set.
			logger.Debug("makeProxy Director: Processing request", "method", req.Method, "path", req.URL.Path, "remote_addr", req.RemoteAddr)

			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			originalPath := req.URL.Path // e.g., /v1/models, /v1/chat/completions
			logger.Debug("makeProxy Director: Original path for proxying", "path", originalPath)

			// For specific paths like /v1/chat/completions, we might need to inspect/modify the body.
			// Currently, no body modifications are performed by default.
			// If body processing is needed for certain paths, it can be added here.
			if originalPath == "/v1/chat/completions" {
				if req.Body != nil && req.Body != http.NoBody {
					bodyBytes, readErr := io.ReadAll(req.Body)
					// After ReadAll, the original req.Body is consumed. We must always replace it.
					// req.Body.Close() is typically handled by ReadAll on success or by the server processing the request.

					if readErr != nil {
						logger.Error("makeProxy Director: Error reading request body", "path", originalPath, "error", readErr)
						// bodyBytes will contain what was read before the error.
						// Replace req.Body with what was read. ContentLength might be inaccurate if read was partial.
						req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
						req.ContentLength = int64(len(bodyBytes))
					} else {
						// Body read successfully. Pass it through without modification.
						logger.Debug("makeProxy Director: Passing original request body", "path", originalPath, "content_length", len(bodyBytes))
						req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
						req.ContentLength = int64(len(bodyBytes))
					}
				}
			}

			// All /v1/* paths are proxied by stripping /v1 and appending to target.Path
			// target.Path is like /v1/projects/PROJECT_ID/locations/LOCATION_ID/endpoints/openapi
			// So, if originalPath is /v1/models, newPath becomes /v1/projects/.../openapi/models
			// If originalPath is /v1/chat/completions, newPath becomes /v1/projects/.../openapi/chat/completions
			// etc.
			if strings.HasPrefix(originalPath, "/v1/") {
				suffixPath := strings.TrimPrefix(originalPath, "/v1")
				req.URL.Path = target.Path + suffixPath
				logger.Debug("makeProxy Director: Rewriting path", "original_path", originalPath, "suffix_path", suffixPath, "new_target_path", req.URL.Path)
			} else {
				// Should not happen if router is configured for /v1/
				logger.Warn("makeProxy Director: Path does not start with /v1/", "path", originalPath)
				// This will effectively make req.URL.Path = target.Path + originalPath
				// For example, if target.Path is /foo and originalPath is /bar, it becomes /foo/bar.
				// If originalPath is just "bar", it becomes /foobar (if target.Path ends with /) or /foo/bar (if target.Path does not end with /)
				// The current target.Path is "/v1/projects/%s/locations/%s/endpoints/openapi"
				// So if originalPath is "/unexpected", it becomes "/v1/projects/.../openapi/unexpected"
				// This behavior is kept for robustness but ideally all requests to this proxy handler start with /v1/.
				req.URL.Path = target.Path + originalPath
			}

			logger.Debug("makeProxy Director: Final target URL for upstream", "url", req.URL.String())

			if tok, err := getToken(req.Context()); err == nil {
				req.Header.Set("Authorization", "Bearer "+tok)
				logger.Debug("makeProxy Director: Authorization header set", "path", req.URL.Path)
			} else {
				logger.Error("Error getting token for request", "path", originalPath, "error", err)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			logger.Debug("makeProxy ModifyResponse: Received response from upstream", "host", resp.Request.URL.Host, "method", resp.Request.Method, "path", resp.Request.URL.Path, "status", resp.Status)
			var upstreamHeaders strings.Builder
			for k, v := range resp.Header {
				upstreamHeaders.WriteString(fmt.Sprintf("\n  %s: %s", k, strings.Join(v, ", ")))
			}
			if upstreamHeaders.Len() > 0 {
				logger.Debug("makeProxy ModifyResponse: Upstream response headers", "headers", upstreamHeaders.String())
			}

			if resp.StatusCode >= 400 {
				bodyBytes, err := io.ReadAll(resp.Body)
				if err != nil {
					logger.Error("makeProxy ModifyResponse: Error reading error response body from upstream", "error", err)
					// Body is already consumed or errored, replace with empty to avoid client issues.
					resp.Body = io.NopCloser(bytes.NewBuffer(nil))
				} else {
					// IMPORTANT: Replace the body so it can be read again by the client,
					// regardless of whether we can decompress it for logging.
					resp.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

					// Now, attempt to log the (potentially decompressed) body.
					if resp.Header.Get("Content-Encoding") == "gzip" {
						gzipReader, err := gzip.NewReader(bytes.NewReader(bodyBytes))
						if err != nil {
							logger.Error("makeProxy ModifyResponse: Error creating gzip reader for error response body", "error", err, "detail", "Logging raw body.")
							logger.Debug("makeProxy ModifyResponse: Upstream error response body (raw gzipped)", "body", string(bodyBytes))
						} else {
							decompressedBodyBytes, err := io.ReadAll(gzipReader)
							if err != nil {
								logger.Error("makeProxy ModifyResponse: Error decompressing gzip error response body", "error", err, "detail", "Logging raw body.")
								logger.Debug("makeProxy ModifyResponse: Upstream error response body (raw gzipped)", "body", string(bodyBytes))
							} else {
								logger.Debug("makeProxy ModifyResponse: Upstream error response body (decompressed)", "body", string(decompressedBodyBytes))
							}
							gzipReader.Close()
						}
					} else {
						logger.Debug("makeProxy ModifyResponse: Upstream error response body", "body", string(bodyBytes))
					}
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// r.URL here is the *target* URL.
			logger.Error("HTTP proxy error", "method", r.Method, "target_url", r.URL.String(), "error", err)
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, fmt.Sprintf("Proxy error connecting to upstream service: %v", err))
		},
	}
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	logger.Debug("handleModels: Received request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)

	defaultModelIDs := []string{
		"google/gemini-2.5-pro-preview-03-25",
		"google/gemini-2.5-flash-preview-04-17",
	}
	modelIDs := defaultModelIDs

	availableModelsStr := os.Getenv("VERTEXAI_AVAILABLE_MODELS")
	if availableModelsStr != "" {
		customModelIDsRaw := strings.Split(availableModelsStr, ",")
		var customModelIDsFiltered []string
		for _, id := range customModelIDsRaw {
			trimmedID := strings.TrimSpace(id)
			if trimmedID != "" {
				customModelIDsFiltered = append(customModelIDsFiltered, trimmedID)
			}
		}

		if len(customModelIDsFiltered) > 0 {
			modelIDs = customModelIDsFiltered
			logger.Info("handleModels: Using custom models from VERTEXAI_AVAILABLE_MODELS", "models", modelIDs)
		} else {
			logger.Warn("handleModels: VERTEXAI_AVAILABLE_MODELS set but empty", "env_var_value", availableModelsStr, "using_default_models", modelIDs)
		}
	} else {
		logger.Info("handleModels: VERTEXAI_AVAILABLE_MODELS not set or empty", "using_default_models", modelIDs)
	}

	currentTime := time.Now().Unix()
	responseModels := make([]Model, len(modelIDs))
	for i, id := range modelIDs {
		responseModels[i] = Model{
			ID:      id,
			Object:  "model",
			Created: currentTime,
			OwnedBy: "google", // Assuming all models specified this way are "ownedBy: google"
		}
	}

	response := ModelList{
		Object: "list",
		Data:   responseModels,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Error("Error encoding models list response", "error", err)
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
	logger.Info("handleModels: Successfully sent models list", "count", len(responseModels))
}

func main() {
	initSlogLogger() // Initialize logger first

	logger.Info("Starting proxy server...")
	location = os.Getenv("VERTEXAI_LOCATION")
	projectID = os.Getenv("VERTEXAI_PROJECT")

	logger.Info("main: Configuration", "vertexai_location", location, "vertexai_project", projectID)

	if location == "" || projectID == "" {
		log.Fatal("VERTEXAI_LOCATION and VERTEXAI_PROJECT env vars must be set")
	}

	// Construct the target URL for the general proxy.
	// It uses the same host structure as the models API.
	proxyHost := fmt.Sprintf(vertexAIAPIHostFormat, location)
	baseURL := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/endpoints/openapi",
		proxyHost, projectID, location,
	)
	target, err := url.Parse(baseURL)
	if err != nil {
		log.Fatalf("main: Error parsing target baseURL '%s': %v", baseURL, err)
	}
	logger.Info("main: Proxy target URL configured", "url", target.String())

	http.HandleFunc("/v1/models", handleModels)
	http.Handle("/v1/", makeProxy(target))

	// Get port from environment variable, default to 8080
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	logger.Info("proxy listening", "address", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("main: ListenAndServe failed: %v", err)
	}
}

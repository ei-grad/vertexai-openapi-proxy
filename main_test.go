package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// This function will be run before any other tests in this package
func TestMain(m *testing.M) {
	// Setup: Initialize the logger
	initSlogLogger()

	// Run all tests
	exitCode := m.Run()

	// Teardown (if any needed)

	// Exit with the tests' exit code
	os.Exit(exitCode)
}

// MockTokenSource is a mock for google.Credentials.TokenSource
type MockTokenSource struct {
	AccessTokenString string
	ExpiryTime        time.Time
	Error             error
}

func (m *MockTokenSource) Token() (*oauth2.Token, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	return &oauth2.Token{
		AccessToken: m.AccessTokenString,
		Expiry:      m.ExpiryTime,
	}, nil
}

func TestGetToken_Cached(t *testing.T) {
	tokenMutex.Lock()
	token = "cached_token"
	expiry = time.Now().Add(time.Hour)
	tokenMutex.Unlock()

	ctx := context.Background()
	gotToken, err := getToken(ctx)
	if err != nil {
		t.Fatalf("getToken() error = %v, wantErr %v", err, false)
	}
	if gotToken != "cached_token" {
		t.Errorf("getToken() gotToken = %v, want %v", gotToken, "cached_token")
	}
}

func TestGetToken_NewFetch(t *testing.T) {
	// Reset global token state for this test
	tokenMutex.Lock()
	token = ""
	expiry = time.Time{}
	tokenMutex.Unlock()

	// Store original FindDefaultCredentials and defer its restoration
	originalFindDefaultCredentials := googleFindDefaultCredentials
	defer func() { googleFindDefaultCredentials = originalFindDefaultCredentials }()

	// Mock google.FindDefaultCredentials
	googleFindDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{
			TokenSource: &MockTokenSource{
				AccessTokenString: "new_token",
				ExpiryTime:        time.Now().Add(time.Hour),
			},
		}, nil
	}

	ctx := context.Background()
	gotToken, err := getToken(ctx)
	if err != nil {
		t.Fatalf("getToken() error = %v, wantErr %v", err, false)
	}
	if gotToken != "new_token" {
		t.Errorf("getToken() gotToken = %v, want %v", gotToken, "new_token")
	}

	tokenMutex.RLock()
	if token != "new_token" {
		t.Errorf("global token not set correctly, got %s, want %s", token, "new_token")
	}
	if expiry.IsZero() {
		t.Error("global expiry not set")
	}
	tokenMutex.RUnlock()
}

func TestMakeProxy(t *testing.T) {
	// Set up environment variables for the test
	// Reset global token state for this test to ensure it fetches a new token
	tokenMutex.Lock()
	token = ""
	expiry = time.Time{}
	tokenMutex.Unlock()

	os.Setenv("VERTEXAI_LOCATION", "us-central1")
	os.Setenv("VERTEXAI_PROJECT", "test-project")
	defer os.Unsetenv("VERTEXAI_LOCATION")
	defer os.Unsetenv("VERTEXAI_PROJECT")

	// Store original FindDefaultCredentials and defer its restoration
	originalFindDefaultCredentials := googleFindDefaultCredentials
	defer func() { googleFindDefaultCredentials = originalFindDefaultCredentials }()

	// Mock google.FindDefaultCredentials
	googleFindDefaultCredentials = func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
		return &google.Credentials{
			TokenSource: &MockTokenSource{
				AccessTokenString: "test-token",
				ExpiryTime:        time.Now().Add(time.Hour),
			},
		}, nil
	}

	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Target server did not receive Authorization header, got: %s", r.Header.Get("Authorization"))
		}
		if !strings.HasSuffix(r.URL.Path, "/testpath") {
			t.Errorf("Target server did not receive correct path, got: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("target server response"))
	}))
	defer targetServer.Close()

	targetURL, _ := url.Parse(targetServer.URL)
	proxy := makeProxy(targetURL)

	req := httptest.NewRequest("GET", "http://localhost/v1/testpath", nil)
	rr := httptest.NewRecorder()

	proxy.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}

	expectedBody := "target server response"
	if rr.Body.String() != expectedBody {
		t.Errorf("handler returned unexpected body: got %v want %v",
			rr.Body.String(), expectedBody)
	}
}

func TestHandleModels(t *testing.T) {
	req, err := http.NewRequest("GET", "/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(handleModels)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v. Body: %s",
			status, http.StatusOK, rr.Body.String())
	}

	expectedContentType := "application/json"
	if contentType := rr.Header().Get("Content-Type"); contentType != expectedContentType {
		t.Errorf("handler returned wrong Content-Type: got %v want %v",
			contentType, expectedContentType)
	}

	var modelListResponse ModelList
	if err := json.NewDecoder(rr.Body).Decode(&modelListResponse); err != nil {
		t.Fatalf("Failed to decode response body: %v", err)
	}

	if modelListResponse.Object != "list" {
		t.Errorf("Expected object 'list', got '%s'", modelListResponse.Object)
	}

	if len(modelListResponse.Data) != 2 {
		t.Fatalf("Expected 2 models in data, got %d", len(modelListResponse.Data))
	}

	// Check first model
	expectedModel1ID := "google/gemini-2.5-pro-preview-03-25"
	model1 := modelListResponse.Data[0]
	if model1.ID != expectedModel1ID {
		t.Errorf("Expected model ID '%s', got '%s'", expectedModel1ID, model1.ID)
	}
	if model1.Object != "model" {
		t.Errorf("Expected model object 'model', got '%s'", model1.Object)
	}
	if model1.OwnedBy != "google" {
		t.Errorf("Expected model owned_by 'google', got '%s'", model1.OwnedBy)
	}
	if model1.Created == 0 {
		t.Errorf("Expected model created timestamp to be non-zero, got %d", model1.Created)
	}

	// Check second model
	expectedModel2ID := "google/gemini-2.5-flash-preview-04-17"
	model2 := modelListResponse.Data[1]
	if model2.ID != expectedModel2ID {
		t.Errorf("Expected model ID '%s', got '%s'", expectedModel2ID, model2.ID)
	}
	if model2.Object != "model" {
		t.Errorf("Expected model object 'model', got '%s'", model2.Object)
	}
	if model2.OwnedBy != "google" {
		t.Errorf("Expected model owned_by 'google', got '%s'", model2.OwnedBy)
	}
	if model2.Created == 0 {
		t.Errorf("Expected model created timestamp to be non-zero, got %d", model2.Created)
	}

	// Ensure the created timestamps are the same as they are set at the same time in the handler
	if model1.Created != model2.Created {
		t.Errorf("Expected model created timestamps to be the same, got %d and %d", model1.Created, model2.Created)
	}
}

func TestHandleModels_CustomEnvVar(t *testing.T) {
	tests := []struct {
		name                string
		envVarValue         string
		expectedModelIDs    []string
		expectDefaultModels bool
	}{
		{
			name:             "Custom models from env var",
			envVarValue:      "custom/model-1, google/model-2 ", // Note trailing space
			expectedModelIDs: []string{"custom/model-1", "google/model-2"},
		},
		{
			name:             "Single custom model from env var",
			envVarValue:      "google/gemini-custom",
			expectedModelIDs: []string{"google/gemini-custom"},
		},
		{
			name:                "Empty env var, fallback to default",
			envVarValue:         "",
			expectDefaultModels: true,
			expectedModelIDs:    []string{"google/gemini-2.5-pro-preview-03-25", "google/gemini-2.5-flash-preview-04-17"},
		},
		{
			name:                "Env var with only commas, fallback to default",
			envVarValue:         ", , ,,",
			expectDefaultModels: true,
			expectedModelIDs:    []string{"google/gemini-2.5-pro-preview-03-25", "google/gemini-2.5-flash-preview-04-17"},
		},
		{
			name:             "Env var with spaces and one valid model",
			envVarValue:      "  custom/model-spaced  ",
			expectedModelIDs: []string{"custom/model-spaced"},
		},
	}

	originalEnvVal := os.Getenv("VERTEXAI_AVAILABLE_MODELS")
	defer os.Setenv("VERTEXAI_AVAILABLE_MODELS", originalEnvVal) // Restore original value

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVarValue != "" || os.Getenv("VERTEXAI_AVAILABLE_MODELS") != "" { // only set if tc.envVarValue is not empty or if it was previously set
				os.Setenv("VERTEXAI_AVAILABLE_MODELS", tc.envVarValue)
			} else {
				os.Unsetenv("VERTEXAI_AVAILABLE_MODELS")
			}

			req, err := http.NewRequest("GET", "/v1/models", nil)
			if err != nil {
				t.Fatal(err)
			}

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(handleModels)
			handler.ServeHTTP(rr, req)

			if status := rr.Code; status != http.StatusOK {
				t.Errorf("handler returned wrong status code: got %v want %v. Body: %s",
					status, http.StatusOK, rr.Body.String())
			}

			var modelListResponse ModelList
			if err := json.NewDecoder(rr.Body).Decode(&modelListResponse); err != nil {
				t.Fatalf("Failed to decode response body: %v", err)
			}

			if len(modelListResponse.Data) != len(tc.expectedModelIDs) {
				t.Fatalf("Expected %d models, got %d. Response: %+v", len(tc.expectedModelIDs), len(modelListResponse.Data), modelListResponse.Data)
			}

			for i, expectedID := range tc.expectedModelIDs {
				if modelListResponse.Data[i].ID != expectedID {
					t.Errorf("Expected model ID '%s' at index %d, got '%s'", expectedID, i, modelListResponse.Data[i].ID)
				}
				if modelListResponse.Data[i].Object != "model" {
					t.Errorf("Expected model object 'model' for ID %s, got '%s'", expectedID, modelListResponse.Data[i].Object)
				}
				if modelListResponse.Data[i].OwnedBy != "google" {
					t.Errorf("Expected model owned_by 'google' for ID %s, got '%s'", expectedID, modelListResponse.Data[i].OwnedBy)
				}
				if modelListResponse.Data[i].Created == 0 {
					t.Errorf("Expected model created timestamp to be non-zero for ID %s", expectedID)
				}
			}
		})
	}
}

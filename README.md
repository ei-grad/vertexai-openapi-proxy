# Vertex AI OpenAI Proxy

## Overview

This project provides a proxy server that translates OpenAI API requests to Google Cloud Vertex AI. It allows applications designed to work with the OpenAI API (like Open WebUI) to use Google's Vertex AI models (e.g., Gemini) without significant modification.

The proxy handles:
- Authentication with Google Cloud using Application Default Credentials (ADC).
- Caching of authentication tokens.
- Serving a static list of available Vertex AI models under the `/v1/models` endpoint.
- Proxying chat completion requests to the appropriate Vertex AI endpoint.

It is designed to be run as a Docker container, typically orchestrated with `docker-compose` alongside an application like Open WebUI.

## Prerequisites

1.  **Google Cloud Project**: You need an active Google Cloud Project with the Vertex AI API enabled.
2.  **Application Default Credentials (ADC)**:
    *   Ensure you have authenticated with Google Cloud CLI: `gcloud auth application-default login`
    *   The proxy relies on the ADC file (typically found at `~/.config/gcloud/application_default_credentials.json` on Linux/macOS) to authenticate with Google Cloud. This file needs to be mounted into the proxy container.
3.  **Environment Variables**:
    *   `VERTEXAI_PROJECT`: Your Google Cloud Project ID.
    *   `VERTEXAI_LOCATION`: The Google Cloud region for Vertex AI (e.g., `us-central1`).
4.  **Docker and Docker Compose**: Required to build and run the service.

## How to Run

The project includes a `docker-compose.yml` file for easy setup with Open WebUI.

1.  **Configure Environment Variables**:
    Ensure `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` are set in your environment or in a `.env` file in the project root. `docker-compose` will automatically pick them up.
    Example `.env` file:
    ```
    VERTEXAI_PROJECT=your-gcp-project-id
    VERTEXAI_LOCATION=us-central1
    ```

2.  **Verify ADC Path (if necessary)**:
    The `docker-compose.yml` mounts `~/.config/gcloud/application_default_credentials.json` by default. If your ADC file is located elsewhere, update the volume mount path for the `proxy` service in `docker-compose.yml`:
    ```yaml
    services:
      proxy:
        # ...
        volumes:
          # IMPORTANT: Replace ~/.config/gcloud/application_default_credentials.json
          # with the actual path to your ADC file if it's different.
          - /path/to/your/adc.json:/app/gcp_adc.json:ro
        # ...
    ```

3.  **Start the Services**:
    ```bash
    docker compose up -d
    ```
    This will build the proxy image (if not already built) and start both the `proxy` and `webui` services.

4.  **Access Open WebUI**:
    Open your browser and navigate to `http://localhost:8080`. Open WebUI should be configured to use the proxy.

## How to Test

The Go application includes unit tests.

1.  **Ensure Go is installed.**
2.  **Navigate to the project directory.**
3.  **Run tests:**
    ```bash
    go test ./...
    ```

## Configuration

### Proxy Service (`main.go`)

The proxy service is configured via environment variables:

*   `VERTEXAI_PROJECT`: (Required) Your Google Cloud Project ID.
*   `VERTEXAI_LOCATION`: (Required) The Google Cloud region for Vertex AI (e.g., `us-central1`).
*   `GOOGLE_APPLICATION_CREDENTIALS`: (Set within `docker-compose.yml`) Points to the path of the mounted ADC JSON file inside the container (e.g., `/app/gcp_adc.json`).
*   `VERTEXAI_AVAILABLE_MODELS`: (Optional) A comma-separated list of model IDs to serve via the `/v1/models` endpoint.
    *   Example: `VERTEXAI_AVAILABLE_MODELS="google/gemini-1.0-pro,google/gemini-1.5-flash-preview-0514"`
    *   If not set or empty, defaults to: `"google/gemini-2.5-pro-preview-03-25,google/gemini-2.5-flash-preview-04-17"`.
    *   Spaces around model IDs and commas are trimmed. Empty entries resulting from multiple commas (e.g. `model1,,model2`) are ignored.

*   `LOG_LEVEL`: (Optional) Sets the logging level.
    *   Supported values: `debug`, `info`, `warn`, `error`.
    *   Defaults to `info` if not set or invalid.
*   `LOG_FORMAT`: (Optional) Sets the log output format.
    *   Supported values: `text` (human-readable), `json` (structured).
    *   Defaults to `text` if not set or invalid.

The proxy listens on port `8080` within its container.

### Open WebUI Service (`docker-compose.yml`)

The `webui` service in `docker-compose.yml` is pre-configured to use the proxy:

*   `OPENAI_API_BASE_URL: http://proxy:8080/v1`
*   `OPENAI_API_KEY: dummy_key_for_vertex_proxy` (The key can be any non-empty string as the proxy handles authentication via ADC).

### Available Models

The list of models served by the `/v1/models` endpoint can be configured using the `VERTEXAI_AVAILABLE_MODELS` environment variable (see "Proxy Service" configuration above).

If `VERTEXAI_AVAILABLE_MODELS` is not set or is empty, the proxy defaults to serving the following models:
*   `google/gemini-2.5-pro-preview-03-25`
*   `google/gemini-2.5-flash-preview-04-17`

All models, whether default or custom, are presented with `object: "model"` and `owned_by: "google"`.

## Logging

The proxy service logs information about incoming requests, token fetching, and upstream communication to standard output.

### Viewing Logs

You can view these logs using:
```bash
docker compose logs proxy
```
Or, if running the Go application directly:
```bash
go run main.go
```

### Configuring Log Output

You can control the verbosity and format of the logs using environment variables:

*   **`LOG_LEVEL`**:
    *   Determines the minimum level of logs to display.
    *   Supported values:
        *   `debug`: Detailed information, useful for troubleshooting.
        *   `info`: Standard operational information (default).
        *   `warn`: Warnings about potential issues.
        *   `error`: Error messages.
    *   Example: To set the log level to debug:
        ```bash
        LOG_LEVEL=debug go run main.go
        ```
        Or in `docker-compose.yml` or your `.env` file:
        ```env
        LOG_LEVEL=debug
        ```

*   **`LOG_FORMAT`**:
    *   Determines the output format of the logs.
    *   Supported values:
        *   `text`: Human-readable, plain text format (default).
        *   `json`: Structured JSON format, suitable for log management systems.
    *   Example: To set the log format to JSON:
        ```bash
        LOG_FORMAT=json go run main.go
        ```
        Or in `docker-compose.yml` or your `.env` file:
        ```env
        LOG_FORMAT=json
        ```

**Example combining both:**
To run with debug level and JSON format:
```bash
LOG_LEVEL=debug LOG_FORMAT=json go run main.go
```
Or in your `.env` file:
```env
LOG_LEVEL=debug
LOG_FORMAT=json
```

## Troubleshooting

*   **Authentication Errors**:
    *   Ensure your ADC file is correctly mounted and `GOOGLE_APPLICATION_CREDENTIALS` inside the container points to it.
    *   Verify the Vertex AI API is enabled in your GCP project.
    *   Check that the service account associated with your ADC (or your user credentials) has the "Vertex AI User" role or equivalent permissions.
*   **"dummy_key_for_vertex_proxy"**: This key is used by Open WebUI to satisfy its requirement for an API key. The actual authentication to Vertex AI is handled by the proxy using Google Cloud ADC.
*   **Model Not Found**: Ensure the model name used in your client application (e.g., Open WebUI) matches one of the models supported by the proxy (e.g., `google/gemini-2.5-pro-preview-03-25`). The client must send the model name with the `google/` prefix if required by the Vertex AI backend, as the proxy no longer automatically prepends it.

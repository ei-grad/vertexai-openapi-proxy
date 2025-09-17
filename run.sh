#!/bin/bash

export VERTEXAI_PROJECT=vertexai-469323
export VERTEXAI_LOCATION=us-east5
export VERTEXAI_AVAILABLE_MODELS="google/gemini-2.5-pro,google/gemini-2.5-flash"
export PORT=6002

./vertexai-proxy

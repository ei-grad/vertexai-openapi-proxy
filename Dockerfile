FROM golang:alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/proxy

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/proxy /app/proxy

# These environment variables are expected by the application.
# They should be set during 'docker run'.
# ENV VERTEXAI_LOCATION=""
# ENV VERTEXAI_PROJECT=""

EXPOSE 8080

ENTRYPOINT ["/app/proxy"]

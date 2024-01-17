#!/bin/bash

# Compiles the fake agent worker process for multiple platforms

mkdir -p macos
mkdir -p linux-amd64
mkdir -p linux-arm64
export CGO_ENABLED=0

GOOS=linux GOARCH=amd64 go build -o linux-amd64/Agent.Worker main.go
GOOS=linux GOARCH=arm64 go build -o linux-arm64/Agent.Worker main.go
GOOS=darwin GOARCH=arm64 go build -o macos/Agent.Worker main.go

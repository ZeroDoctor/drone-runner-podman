#!/bin/sh

# disable go modules
export GOPATH=""

# enable cgo due to 
# https://github.com/containers/image/issues/1382
# which means difficult times ahead with cross compiling
export CGO_ENABLED=1

set -e
set -x

# linux - btw, I use amd64 arch linux... okay its manjaro... close enough
GOOS=linux GOARCH=amd64 go build -o release/linux/amd64/drone-runner-podman
# GOOS=linux GOARCH=arm64 go build -o release/linux/arm64/drone-runner-podman
# GOOS=linux GOARCH=arm   go build -o release/linux/arm/drone-runner-podman
# GOOS=linux GOARCH=ppc64le go build -o release/linux/ppc64le/drone-runner-podman

# windows
# GOOS=windows go build -o release/windows/amd64/drone-runner-podman.exe

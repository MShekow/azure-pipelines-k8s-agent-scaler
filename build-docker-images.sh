#!/bin/bash

set -e

cd "$(dirname "$0")"

CONTROLLER_IMAGE_NAME=${1:-controller:latest}
FAKE_AGENT_IMAGE_NAME=${2:-fake-agent:local}

docker buildx create --driver docker-container --name mybuilder || true

docker build --builder mybuilder -t $CONTROLLER_IMAGE_NAME --load .
docker build --builder mybuilder -t $FAKE_AGENT_IMAGE_NAME -f fake_agent/Dockerfile --load .

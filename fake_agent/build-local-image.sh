#!/bin/bash

set -e

cd ..

docker buildx create --driver docker-container --name mybuilder || true

# k3d cluster create foo --registry-create foo-registry:0.0.0.0:5001
docker build --builder mybuilder -t localhost:5001/fake-agent:local -f fake_agent/Dockerfile --load .
# docker push localhost:5001/fake-agent:local

# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.21 as builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

COPY . .

RUN cd fake_agent && go mod download
RUN cd fake_agent && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o fake_agent main.go

RUN cd fake_agent_worker_process && go mod download
RUN cd fake_agent_worker_process && ./compile.sh && cp linux-${TARGETARCH}/Agent.Worker /workspace/Agent.Worker


FROM gcr.io/distroless/static:debug-nonroot
ARG TARGETARCH
WORKDIR /
COPY --from=builder /workspace/fake_agent/fake_agent .
COPY --from=builder /workspace/Agent.Worker /Agent.Worker

ENV WORKER_BINARY=/Agent.Worker

USER 65532:65532

ENTRYPOINT ["./fake_agent"]

#based on https://docs.microsoft.com/en-us/azure/devops/pipelines/agents/docker?view=azure-devops#create-and-build-the-dockerfile-1
FROM ubuntu:20.04

#install agent dependencies
RUN apt update && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
    apt-transport-https \
    apt-utils \
    ca-certificates \
    curl \
    gnupg \
    git \
    git-lfs \
    iputils-ping \
    jq \
    zip \
    lsb-release \
    software-properties-common \
    libicu66

ARG TARGETARCH

#install kubectl to use kubeexec to access other containers inside pod
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" && install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl && kubectl version --client

#install agent dependencies
RUN curl -sL 'https://azurecliprod.blob.core.windows.net/$root/deb_install.sh' | bash

#download agent and setup startup
# ENV TARGETARCH='linux-x64'
# ENV TARGETARCH='linux-arm64'
WORKDIR /azp

ENV AGENT_VERSION="3.224.1"
ENV AGENT_PLATFORM=${TARGETARCH}
RUN if [ "${TARGETARCH}" = "amd64" ]; then export AGENT_PLATFORM='x64'; fi; curl -LsS https://vstsagentpackage.azureedge.net/agent/${AGENT_VERSION}/vsts-agent-linux-${AGENT_PLATFORM}-${AGENT_VERSION}.tar.gz | tar -xz & wait $!

RUN echo $AGENT_PLATFORM

COPY ./*.sh .
RUN chmod +x start.sh execContainer.sh

ENTRYPOINT [ "./start.sh" ]

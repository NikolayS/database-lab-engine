#!/bin/bash
set -euxo pipefail

# Install dependencies
apt-get update && apt-get install -y \
  apt-transport-https \
  ca-certificates \
  curl \
  gnupg-agent \
  software-properties-common \
  sudo

# Install Docker
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add -

sudo add-apt-repository \
  "deb [arch=amd64] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) \
  stable"

sudo apt-get install -y \
  docker-ce \
  docker-ce-cli \
  containerd.io

# Install ZFS
sudo apt-get install -y \
  zfsutils-linux

# Install psql
sudo apt-get install -y \
  postgresql-client

# Install yq
if ! command -v yq &> /dev/null; then
  sudo curl -L https://github.com/mikefarah/yq/releases/download/v4.14.2/yq_linux_amd64 -o /usr/bin/yq && sudo chmod +x /usr/bin/yq
fi
# TODO: Uncomment after upgrade the runner machine
# sudo apt-key adv --keyserver keyserver.ubuntu.com --recv-keys CC86BB64
# sudo add-apt-repository ppa:rmescandon/yq
# sudo apt-get update && sudo apt-get install yq -y

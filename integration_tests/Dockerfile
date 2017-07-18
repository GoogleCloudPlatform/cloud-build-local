FROM gcr.io/cloud-builders/go:wheezy

# Install Docker
# Based on instructions from:
# https://docs.docker.com/engine/installation/linux/ubuntu/
RUN \
   apt-get -y update && \
   apt-get -y install apt-transport-https ca-certificates curl \
       # These are necessary for add-apt-respository
       software-properties-common python-software-properties && \
   curl -fsSL https://yum.dockerproject.org/gpg | sudo apt-key add - && \
   apt-key fingerprint 58118E89F3A912897C070ADBF76221572C52609D && \
   add-apt-repository \
       "deb https://apt.dockerproject.org/repo/ \
       ubuntu-$(lsb_release -cs) \
       main" && \
   apt-get -y update

ARG DOCKER_VERSION=17.05.0~ce-0~ubuntu-trusty
RUN apt-get -y install docker-engine=${DOCKER_VERSION}

# Install gcloud
RUN apt-get -y update && \
    apt-get -y install gcc python2.7 python-dev python-setuptools wget ca-certificates \
       # These are necessary for add-apt-respository
       software-properties-common python-software-properties && \

    # Install Git >2.0.1
    add-apt-repository ppa:git-core/ppa && \
    apt-get -y update && \
    apt-get -y install git && \

    # Setup Google Cloud SDK (latest)
    mkdir -p /builder && \
    wget -qO- https://dl.google.com/dl/cloudsdk/release/google-cloud-sdk.tar.gz | tar zxv -C /builder && \
    CLOUDSDK_PYTHON="python2.7" /builder/google-cloud-sdk/install.sh --usage-reporting=false \
        --bash-completion=false \
        --disable-installation-options && \

    # Install additional components
    /builder/google-cloud-sdk/bin/gcloud -q components install \
        alpha beta kubectl && \
    /builder/google-cloud-sdk/bin/gcloud -q components update && \

    # install crcmod: https://cloud.google.com/storage/docs/gsutil/addlhelp/CRC32CandInstallingcrcmod
    easy_install -U pip && \
    pip install -U crcmod && \

    # Clean up
    apt-get -y remove gcc python-dev python-setuptools wget && \
    rm -rf /var/lib/apt/lists/* && \
    rm -rf ~/.config/gcloud

ENV PATH=/builder/google-cloud-sdk/bin/:$PATH

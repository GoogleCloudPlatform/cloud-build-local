successful_startup=0

# Fetch some metadata.
zone=$(curl -H"Metadata-flavor: Google" metadata.google.internal/computeMetadata/v1/instance/attributes/zone)
gcs_path=$(curl -H"Metadata-flavor: Google" metadata.google.internal/computeMetadata/v1/instance/attributes/gcs_path)
gcs_logs_path=$(curl -H"Metadata-flavor: Google" metadata.google.internal/computeMetadata/v1/instance/attributes/gcs_logs_path)

function uploadLogs() {
  touch startuplog.txt || exit
  sudo fgrep startup-script /var/log/syslog > startuplog.txt
  # Rename the file .txt so that it opens rather than downloading when clicked in Pantheon.
  gsutil cp startuplog.txt $gcs_logs_path/startup.txt
  [[ $successful_startup == 0 ]] && (
    gsutil cp startuplog.txt $gcs_logs_path/startup-failure.txt ||
    "$DOWNLOAD_DIR/old_sdk"/bin/gsutil cp startuplog.txt $gcs_logs_path/startup-failure.txt
  )
}
trap uploadLogs EXIT INT TERM

set -x

# Install the Cloud SDK
gcloud info || exit

function install_sdk() {
  export CLOUDSDK_CORE_DISABLE_PROMPTS=1
  export CLOUDSDK_INSTALL_DIR=/usr/lib

  # We use the public installer.
  rm -rf "$CLOUDSDK_INSTALL_DIR/google-cloud-sdk"
  curl https://sdk.cloud.google.com | bash || exit
}
install_sdk&
# add the install_sdk PID to the list for waiting.
pids="$! $pids"

function install_docker() {
  echo "Installing docker..."
  sudo apt-get update || exit
  sudo apt-get install \
    linux-image-extra-$(uname -r) \
    linux-image-extra-virtual \
    apt-transport-https \
    ca-certificates \
    curl \
    software-properties-common || exit
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo apt-key add - || exit
  sudo add-apt-repository \
    "deb [arch=amd64] https://download.docker.com/linux/ubuntu \
    $(lsb_release -cs) \
    stable" || exit
  sudo apt-get update || exit
  sudo apt-get install docker-ce || exit
  # Test.
  docker --version || exit

  # # Add docker-engine, using directions at https://docs.docker.com/engine/installation/ubuntulinux/
  # sudo apt-key adv --keyserver hkp://p80.pool.sks-keyservers.net:80 --recv-keys 58118E89F3A912897C070ADBF76221572C52609D || exit
  # echo "deb https://apt.dockerproject.org/repo ubuntu-trusty main" >> /etc/apt/sources.list.d/docker.list || exit
  # # apt-get update appears to be a bit flakey with requests to our GCS
  # # mirrors timing out. Try a few times just in case.
  # sudo apt-get update || sudo apt-get update || sudo apt-get update || exit
  # # The docker-engine= is the key to setting the version. If you set up apt to
  # # use the https://apt.dockerproject.org/repo ubuntu-trusty as described
  # # above, "apt-cache madison docker-engine" will give you a list of the
  # # currently available versions.
  # sudo apt-get install -y docker-engine=17.05.0~ce-0~ubuntu-trusty unzip tcpdump || exit
}
install_docker&
# add the install_docker PID to the list for waiting.
pids="$! $pids"

wait $pids || exit
successful_startup=1

# Set a metadata value about the success of the startup script.
gcloud config set compute/zone ${zone}
gcloud compute instances add-metadata $HOSTNAME --metadata=successful_startup=1

# Fetch test files from gcs.
mkdir /root/test-files
gsutil -m copy ${gcs_path}/* /root/test-files/
chmod +x /root/test-files/test-script.sh || exit

# Copy local builder binary to bin.
chmod +x /root/test-files/container-builder-local || exit
mv /root/test-files/container-builder-local /usr/local/bin/

# Copy up an empty output.txt as a signal to the runner that the script is starting.
touch /root/output.txt || exit
gsutil cp /root/output.txt $gcs_logs_path/output.txt || exit

# Run the integration test script. When finished, write to a "success" or "failure" file
# in GCS so that the test runner can stop immediately.
(
  # If the test succeeds, copy the output to success.txt. Else, to failure.txt.
  cd /root/test-files
  ./test-script.sh &> /root/output.txt && \
    gsutil cp /root/output.txt $gcs_logs_path/success.txt || \
    gsutil cp /root/output.txt $gcs_logs_path/failure.txt
  touch done
)&

# Concurrently with the test, periodically copy the output to GCS so that it can be
# inspected.
(
  # Every 1s, write the output-to-date into GCS for inspection.
  while [[ ! -f done ]]; do
    sleep 1s
    gsutil cp /root/output.txt $gcs_logs_path/output.txt
  done
)&

wait

# Copy the output of this script into GCS as well. Note that we can't rely on
# the above TRAP to upload because we're about to kill the VM.
uploadLogs

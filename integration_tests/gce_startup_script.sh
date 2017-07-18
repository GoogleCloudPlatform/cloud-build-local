successful_startup=-1
successful_test=-1

function uploadLogs() {
  sudo fgrep startup-script /var/log/syslog > startuplog
  # Rename the file .txt so that it opens rather than downloading when clicked in Pantheon.
  gsutil cp startuplog gs://container-builder-local-test-logs/startup.txt
  [[ $successful_startup == -1 ]] && (
    gsutil cp startuplog gs://container-builder-local-test-logs/startup-failure.txt ||
    "$DOWNLOAD_DIR/old_sdk"/bin/gsutil cp startuplog gs://container-builder-local-test-logs/startup-failure.txt
  )
}
trap uploadLogs EXIT INT TERM

# Install Stackdriver Logging Agent https://cloud.google.com/logging/docs/agent/installation
curl -sSO https://dl.google.com/cloudagents/install-logging-agent.sh || exit
echo '8db836510cf65f3fba44a3d49265ed7932e731e7747c6163da1c06bf2063c301  install-logging-agent.sh' > checksum
sha256sum -c checksum || exit
bash install-logging-agent.sh || exit
rm -rf install-logging-agent.sh checksum

# Configure Stackdriver Logging Agent to capture our logs.
# https://cloud.google.com/logging/docs/agent/installation#configure
cat > /etc/google-fluentd/config.d/gcetesting.conf << EOF
<source>
  type tail
  format none
  path /root/output.txt
  pos_file /var/lib/google-fluentd/pos/gcetesting.pos
  read_from_head true
  tag gcetesting
</source>
EOF
chmod 0644 /etc/google-fluentd/config.d/gcetesting.conf

# Pick up the configuration change.
service google-fluentd reload

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
  # Add docker-engine, using directions at https://docs.docker.com/engine/installation/ubuntulinux/
  sudo apt-key adv --keyserver hkp://p80.pool.sks-keyservers.net:80 --recv-keys 58118E89F3A912897C070ADBF76221572C52609D || exit
  echo "deb https://apt.dockerproject.org/repo ubuntu-trusty main" >> /etc/apt/sources.list.d/docker.list || exit
  # apt-get update appears to be a bit flakey with requests to our GCS
  # mirrors timing out. Try a few times just in case.
  sudo apt-get update || sudo apt-get update || sudo apt-get update || exit
  # The docker-engine= is the key to setting the version. If you set up apt to
  # use the https://apt.dockerproject.org/repo ubuntu-trusty as described
  # above, "apt-cache madison docker-engine" will give you a list of the
  # currently available versions.
  sudo apt-get install -y docker-engine=17.05-ce unzip tcpdump || exit
}
install_docker&
# add the install_docker PID to the list for waiting.
pids="$! $pids"

wait $pids || exit
successful_startup=0 # success

# Fetch some metadata.
zone=$(curl -H"Metadata-flavor: Google" metadata.google.internal/computeMetadata/v1/instance/attributes/zone)

# Set a metadata value about the success of the startup script.
gcloud config set compute/zone ${zone}
gcloud compute instances add-metadata $HOSTNAME --metadata=successful_startup=${successful_startup}

# Fetch test files from gcs.
mkdir /root/test-files
gsutil -m copy gs://container-builder-local-test/* /root/test-files/
chmod +x /root/test-files/container-builder-local || exit
mv /root/test-files/container-builder-local /usr/local/bin/
chmod +x /root/test-files/test-script.sh || exit

# Copy up an empty output.txt as a signal to the runner that the script is starting.
touch /root/output.txt || exit
gsutil cp /root/output.txt gs://container-builder-local-test-logs/output.txt || exit

# Run the integration test script. When finished, write to a "success" or "failure" file
# in GCS so that the test runner can stop immediately.
(
  # If the test succeeds, copy the output to success.txt. Else, to failure.txt.
  cd /root/test-files
  ./test-script.sh &> /root/output.txt && \
    (gsutil cp /root/output.txt gs://container-builder-local-test-logs/success.txt && successful_test=0) || \
    (gsutil cp /root/output.txt gs://container-builder-local-test-logs/failure.txt && successful_test=1)
  echo "did it work? " $successful_test
  gcloud compute instances add-metadata $HOSTNAME --metadata=successful_test=${successful_test}
  touch done
)&

# Concurrently with the test, periodically copy the output to GCS so that it can be
# inspected.
# (
#   # Every 1s, write the output-to-date into GCS for inspection.
#   while [[ ! -f done ]]; do
#     sleep 1s
#     gsutil cp /root/output.txt gs://container-builder-local-test-logs/output.txt
#   done
# )&

wait

# Copy the output of this script into GCS as well. Note that we can't rely on
# the above TRAP to upload because we're about to kill the VM.
uploadLogs


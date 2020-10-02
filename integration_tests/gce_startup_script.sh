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
export CLOUDSDK_CORE_DISABLE_PROMPTS=1
snap refresh google-cloud-sdk || exit
gcloud info || exit
gcloud components install docker-credential-gcr --quiet || exit

# Install docker
docker --version || snap install docker || exit
successful_startup=1

# Set gcloud zone
gcloud config set compute/zone ${zone}

# Fetch test files from gcs.
mkdir /root/test-files
gsutil -m copy ${gcs_path}/* /root/test-files/
chmod +x /root/test-files/test_script.sh || exit

# Copy local builder binary to bin.
chmod +x /root/test-files/cloud-build-local || exit
mv /root/test-files/cloud-build-local /usr/local/bin/

# Copy up an empty output.txt as a signal to the runner that the script is starting.
touch /root/output.txt || exit
gsutil cp /root/output.txt $gcs_logs_path/output.txt || exit

# Run the integration test script. When finished, write to a "success" or "failure" file
# in GCS so that the test runner can stop immediately.
(
  # If the test succeeds, copy the output to success.txt. Else, to failure.txt.
  cd /root/test-files
  export PROJECT_ID=$(gcloud config list --format='value(core.project)')
  ./test_script.sh &> /root/output.txt && \
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

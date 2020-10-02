# Have new buckets for each test.
DATE=`date +%Y%m%d-%H%M%S`
GCS_PATH=gs://local-builder-test/$DATE
GCS_LOGS_PATH=gs://local-builder-test-logs/$DATE
gsutil -m copy cloud-build-local $GCS_PATH/
gsutil -m copy ./integration_tests/* $GCS_PATH/

# Create a VM with startup script.
gcloud config set compute/zone us-central1-f
instance_name=ubuntu-integration-tests-$DATE
gcloud compute instances create $instance_name \
  --image-family=ubuntu-2004-lts \
  --image-project=ubuntu-os-cloud \
  --machine-type=n1-standard-2 \
  --scopes default,userinfo-email,cloud-platform \
  --metadata-from-file startup-script=integration_tests/gce_startup_script.sh \
  --metadata zone=us-central1-f,gcs_path=$GCS_PATH,gcs_logs_path=$GCS_LOGS_PATH || exit

# Wait until either the success or failure file is written to GCS.
status=0
while true
do
  if gsutil stat $GCS_LOGS_PATH/success.txt 2> /dev/null; then
    gsutil cat $GCS_LOGS_PATH/success.txt
    break
  fi
  if gsutil stat $GCS_LOGS_PATH/failure.txt 2> /dev/null; then
    gsutil cat $GCS_LOGS_PATH/failure.txt
    status=1
    break
  fi
  echo "Tests are still running, checking in a few seconds..."
  sleep 5
done

# Delete the VM and return the status
gcloud compute instances delete $instance_name --quiet
exit $status

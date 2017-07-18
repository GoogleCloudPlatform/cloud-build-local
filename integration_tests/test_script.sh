# This file contains the local builder tests.

# Set project.
gcloud config set project $PROJECT_ID

# Get gcr credentials.
echo "path: " $PATH
docker-credential-gcr configure-docker || exit


# Flags tests.
container-builder-local --version || exit
container-builder-local --help || exit
container-builder-local && exit # no source
container-builder-local . --config=cloudbuild_nil.yaml && exit # flags after source
container-builder-local --config=donotexist.yaml . && exit # non-existent config file
container-builder-local --config=cloudbuild_nil.yaml . || exit # happy dryrun case

# End to end tests.
container-builder-local --config=cloudbuild_nil.yaml --dryrun=false . || exit
container-builder-local --config=cloudbuild_dockerfile.yaml --dryrun=false . || exit
container-builder-local --config=cloudbuild_gcr.yaml --dryrun=false --push=true . || exit

exit 0

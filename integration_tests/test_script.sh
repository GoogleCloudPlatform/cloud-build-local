# This file contains the local builder tests.

# Set project.
gcloud config set project $PROJECT_ID

# Configure docker with gcr credentials.
docker-credential-gcr configure-docker || exit

# Flags tests.
container-builder-local --version || exit
container-builder-local --help || exit
container-builder-local && exit # no source
container-builder-local . --config=cloudbuild_nil.yaml && exit # flags after source
container-builder-local --config=donotexist.yaml . && exit # non-existent config file
container-builder-local --config=cloudbuild_nil.yaml . || exit # happy dryrun case
container-builder-local --config=cloudbuild_nil.yaml --write-workspace=/tmp/workspace . || exit # happy dryrun case
if [ ! -f /tmp/workspace/cloudbuild_nil.yaml ]; then
  echo "Exported file not found!"
fi

# Valid substitutions
container-builder-local --config=cloudbuild_substitutions.yaml --substitutions=_MESSAGE="bye world" . || exit
container-builder-local --config=cloudbuild_substitutions.yaml --substitutions=COMMIT_SHA="my-sha" . || exit
# Invalid substitutios are expected to exit with an error (hence the `&& exit`).
container-builder-local --config=cloudbuild_substitutions.yaml --substitutions=PROJECT_ID="my-project" . && exit
container-builder-local --config=cloudbuild_builtin_substitutions.yaml . && exit

# End to end tests.
container-builder-local --config=cloudbuild_nil.yaml --dryrun=false . || exit
container-builder-local --config=cloudbuild_nil.yaml --dryrun=false --no-source=true || exit
container-builder-local --config=cloudbuild_nil.yaml --dryrun=false --no-source=true . && exit
container-builder-local --config=cloudbuild_dockerfile.yaml --dryrun=false . || exit
container-builder-local --config=cloudbuild_gcr.yaml --dryrun=false --push=true . || exit
container-builder-local --config=cloudbuild_big.yaml --dryrun=false --push=true . || exit
container-builder-local --config=cloudbuild_volumes.yaml --dryrun=false . || exit

# Confirm that we set up credentials account correctly.
WANT=$(gcloud config list --format="value(core.account)")
OUT=$(container-builder-local --config=cloudbuild_auth.yaml --dryrun=false .)
if [[ ${OUT} =~ .*${WANT}.* ]]
then
  echo "PASS: auth setup"
else
  echo "FAIL: auth setup"
  exit 1
fi

exit 0

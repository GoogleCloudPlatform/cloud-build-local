# This file contains the local builder tests.

# Set project.
gcloud config set project $PROJECT_ID

# Flags tests.
cloud-build-local --version || exit
cloud-build-local --help || exit
cloud-build-local && exit # no source
cloud-build-local . --config=cloudbuild_nil.yaml && exit # flags after source
cloud-build-local --config=donotexist.yaml . && exit # non-existent config file
cloud-build-local --config=cloudbuild_nil.yaml . || exit # happy dryrun case
cloud-build-local --config=cloudbuild_nil.yaml --write-workspace=/tmp/workspace . || exit # happy dryrun case
if [ ! -f /tmp/workspace/cloudbuild_nil.yaml ]; then
  echo "Exported file not found!"
fi

# Valid substitutions
cloud-build-local --config=cloudbuild_substitutions.yaml --substitutions=_MESSAGE="bye world" . || exit
cloud-build-local --config=cloudbuild_substitutions.yaml --substitutions=COMMIT_SHA="my-sha" . || exit
cloud-build-local --config=cloudbuild_substitutions2.yaml --substitutions=_SUBSTITUTE_ME="literally-anything-else" . || exit
cloud-build-local --config=cloudbuild_substitutions2.yaml --substitutions=_MESSAGE="bye world",_SUBSTITUTE_ME="literally-anything-else" . || exit
cloud-build-local --config=cloudbuild_substitutions.yaml --substitutions=_MESSAGE="substitution set in command line only" . || exit
# Invalid substitutios are expected to exit with an error (hence the `&& exit`).
cloud-build-local --config=cloudbuild_substitutions.yaml --substitutions=PROJECT_ID="my-project" . && exit
cloud-build-local --config=cloudbuild_builtin_substitutions.yaml . && exit

# End to end tests.
cloud-build-local --config=cloudbuild_nil.yaml --dryrun=false . || exit
cloud-build-local --config=cloudbuild_nil.yaml --dryrun=false --no-source=true || exit
cloud-build-local --config=cloudbuild_nil.yaml --dryrun=false --no-source=true . && exit
cloud-build-local --config=cloudbuild_dockerfile.yaml --dryrun=false . || exit
cloud-build-local --config=cloudbuild_gcr.yaml --dryrun=false --push=true . || exit
cloud-build-local --config=cloudbuild_big.yaml --dryrun=false --push=true . || exit
cloud-build-local --config=cloudbuild_volumes.yaml --dryrun=false . || exit
cloud-build-local --config=cloudbuild_buildid.yaml --dryrun=false . || exit

# Confirm that we set up credentials account correctly.
WANT=$(gcloud config list --format="value(core.account)")
OUT=$(cloud-build-local --config=cloudbuild_auth.yaml --dryrun=false .)
if [[ ${OUT} =~ .*${WANT}.* ]]
then
  echo "PASS: auth setup"
else
  echo "FAIL: auth setup"
  exit 1
fi

exit 0

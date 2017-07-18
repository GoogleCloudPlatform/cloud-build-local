# Set project.
gcloud config set project argo-local-builder

# Flags tests.
container-builder-local --version || exit
container-builder-local --help || exit
container-builder-local && exit # no source
container-builder-local . --config=cloudbuild_nil.yaml && exit # flags after source
container-builder-local --config=donotexist.yaml . && exit # unexisting config file
echo "MOD 1"
container-builder-local --config=cloudbuild_nil.yaml . || exit # happy dryrun case

# End to end tests.
echo "MOD 2"
container-builder-local --config=cloudbuild_nil.yaml --dryrun=false . || exit
echo "MOD 3"
#container-builder-local --config=cloudbuild_dockerfile.yaml --dryrun=false . || exit
echo "MOD 4"
#container-builder-local --config=cloudbuild_gcr.yaml --dryrun=false . || exit
echo "MOD 5"

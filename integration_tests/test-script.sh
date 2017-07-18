echo "Hello Philmod"

ls

# Check the flags.
container-builder-local --version 2>&1 || exit
container-builder-local --help 2>&1 || exit
container-builder-local 2>&1 && exit # no source
container-builder-local . --config cloudbuild.yaml 2>&1 && exit # flags after source
container-builder-local --config donotexist.yaml 2>&1 && exit # unexisting config file
container-builder-local --config cloudbuild.yaml . 2>&1 || exit # happy dryrun case

# Full tests.
container-builder-local --config cloudbuild_nil.yaml --dryrun=false . 2>&1 || exit

exit 0

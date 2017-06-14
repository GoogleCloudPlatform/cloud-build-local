mkdir vendor || exit
imports=$(go list -f '{{range .Imports}}{{printf "%s\n" .}}{{end}}' ./... | grep -v "github.com/GoogleCloudPlatform/container-builder-local" )
export GOPATH=$PWD/vendor
set -x
for import in $imports; do
  go get "$import" || exit
done
mv vendor/src/* vendor
rm -r vendor/src vendor/pkg
find vendor/ -type d | grep ".git$" | xargs rm -rf

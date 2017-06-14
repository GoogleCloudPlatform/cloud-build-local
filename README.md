# Google Cloud Container Builder Local Builder

----

This repository contains the code to run
[Google Cloud Container Builder] builds locally.

----

## Using Local Builder

Support for Local Builder with the `gcloud` command-line tool is coming soon.

## Developing Local Builder

To build and develop Local Builder, you need a working [Go environment].

```
go get github.com/GoogleCloudPlatform/container-builder-local
go install github.com/GoogleCloudPlatform/container-builder-local
```

Run a build:
```
./bin/container-builder-local --dryrun=false --config=path/to/cloudbuild.yaml path/to/code
```

Run the tests (without the vendored libraries):
```
go test $(go list github.com/GoogleCloudPlatform/container-builder-local/... | grep -v vendor)
```

## Support

File issues here, or e-mail `gcr-contact@google.com` if you have general
questions about the usage of this code or the Cloud Container Builder.

[Google Cloud Container Builder]: http://cloud.google.com/container-builder/
[Go environment]: https://golang.org/doc/install

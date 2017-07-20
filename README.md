# Google Cloud Container Builder Local Builder

**Local Builder** runs [Google Cloud Container Builder] builds locally, allowing faster debugging, less vendor lock-in,
and integration into local build and test workflows.

----

## Using Local Builder

Support for Local Builder with the `gcloud` command-line tool is coming soon.

## Developing Local Builder

To build and develop Local Builder, you need a working [Go environment].

Run the following commands to install the tool:
```
go get github.com/GoogleCloudPlatform/container-builder-local
go install github.com/GoogleCloudPlatform/container-builder-local
```

To run a build:
```
./bin/container-builder-local --dryrun=false --config=path/to/cloudbuild.yaml path/to/code
```

To run the tests (without the vendored libraries):
```
go test $(go list github.com/GoogleCloudPlatform/container-builder-local/... | grep -v vendor)
```

## Support

File issues here, or e-mail `gcr-contact@google.com` if you have general
questions about Local Builder or Container Builder.

[Google Cloud Container Builder]: http://cloud.google.com/container-builder/
[Go environment]: https://golang.org/doc/install

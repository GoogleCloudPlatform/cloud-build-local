# Google Cloud Container Builder Local Builder

**Local Builder** runs [Google Cloud Container Builder] builds locally,
allowing easier debugging, execution of builds on your own hardware,
and integration into local build and test workflows.

----

## Prerequisites
- gcloud
- docker
- go (if you build the tool yourself)

If the build needs to access a private GCR registry, install and configure the
[Docker credential helper for GCR users](https://github.com/GoogleCloudPlatform/docker-credential-gcr).

The local builder uses gcloud information to set up a spoofed metadata server,
so you have to set the project:
```
gcloud config set project my-project
```

## Download the binary

The latest binaries are available in a GCS bucket.

First check the latest tag on the [releases page](https://github.com/GoogleCloudPlatform/container-builder-local/releases).

```
gsutil cp gs://container-builder-local/container-builder-local_{linux,darwin}_{386,amd64}-v<enter_tag> .
chmod +x container-builder-local_{linux,darwin}_{386,amd64}-v<enter_tag>
```

For example, to run a build on linux 386 with the `v0.0.1` release:

```
gsutil cp gs://container-builder-local/container-builder-local_linux_386-v0.0.1 .
chmod +x container-builder-local_linux_386-v0.0.1
```

To run a build:

```
./container-builder-local_{linux,darwin}_{386,amd64}-v<enter_tag> --dryrun=false --config=path/to/cloudbuild.yaml path/to/code
```

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

## Limitations

- Only one build can be run at a time on a given host.
- The tool doesn't work on Windows yet, PR welcome.

## Support

File issues here, e-mail `gcr-contact@google.com`, or join our [Slack channel]
if you have general questions about Local Builder or Container Builder.

[Google Cloud Container Builder]: http://cloud.google.com/container-builder/
[Go environment]: https://golang.org/doc/install
[Slack channel]: https://googlecloud-community.slack.com/messages/C4KCRJL4D/details/

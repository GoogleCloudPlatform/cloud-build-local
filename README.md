# Google Container Builder Local Builder

**Local Builder** runs [Google Container Builder] locally,
allowing easier debugging, execution of builds on your own hardware,
and integration into local build and test workflows.

----

## Prerequisites

1.  Ensure you have installed:
    - [gcloud](https://cloud.google.com/sdk/docs/quickstarts)
    - [docker](https://www.docker.com/)
    - go (if you want to build the tool yourself)

2.  If the build needs to access a private Google Container Registry, install and
    configure the [Docker credential helper](https://github.com/GoogleCloudPlatform/docker-credential-gcr)
    for Google Container Registry.

3.  Configure your project for the gcloud tool, where [PROJECT_ID] is
    your Cloud Platform project ID:
    
    ```
    gcloud config set project [PROJECT-ID]
    ```

## Install using gcloud

1.  Install by running the following command:

    ```
    gcloud components install container-builder-local
    ```

    After successful installation, you will have `container-builder-local` setup
    on your PATH (as part of the Google Cloud SDK binaries).

2.  To see all of the commands, run:
    
    ```
    $ container-builder-local --help
    ```
    
    The Local Builder's command is `$ container-builder-local`. 
    
## Download the latest binaries

The latest binaries are available in a GCS bucket.

[Download](https://storage.googleapis.com/container-builder-local/container-builder-local_latest.tar.gz) the latest binaries from GCS.

To run a build:

```
./container-builder-local_{linux,darwin}_{386,amd64}-v<latest_tag> --dryrun=false --config=path/to/cloudbuild.yaml path/to/code
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

File issues here on GitHub, email `gcr-contact@google.com`, or join our [Slack channel]
if you have general questions about Local Builder or Container Builder.

[Google Container Builder]: http://cloud.google.com/container-builder/
[Go environment]: https://golang.org/doc/install
[Slack channel]: https://googlecloud-community.slack.com/messages/C4KCRJL4D/details/

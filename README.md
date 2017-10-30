# Google Container Builder Local Builder

**Local Builder** runs [Google Container Builder] locally,
allowing easier debugging, execution of builds on your own hardware,
and integration into local build and test workflows.

----

## Prerequisites

1.  Ensure you have installed:
    * [gcloud](https://cloud.google.com/sdk/docs/quickstarts)
    * [Docker](https://www.docker.com/)
    * [Go](https://golang.org/doc/install) (if you want to compile Local
      Builder from source)

2.  If the build needs to access a private Google Container Registry, install
    and configure the
    [Docker credential helper](https://github.com/GoogleCloudPlatform/docker-credential-gcr)
    for Google Container Registry.

3.  Configure your project for the gcloud tool, where `[PROJECT_ID]` is
    your Cloud Platform project ID:

    ```
    gcloud config set project [PROJECT-ID]
    ```

## Install using gcloud

1.  Install by running the following command:

    ```
    gcloud components install container-builder-local
    ```

    After successful installation, you will have `container-builder-local` in
    your PATH as part of the Google Cloud SDK binaries.

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

## Developing and contributing to the Local Builder

See the [contributing instructions](https://github.com/GoogleCloudPlatform/container-builder-local/blob/master/CONTRIBUTING.md).

## Limitations

*  Only one build can be run at a time on a given host.
*  The tool works on the following platforms:
   * Linux
   * macOS

## Support

File issues here on gitHub, email `gcr-contact@google.com`, or join our
[Slack channel] if you have general questions about Local Builder or
Container Builder.

[Google Container Builder]: http://cloud.google.com/container-builder/
[Slack channel]: https://googlecloud-community.slack.com/messages/C4KCRJL4D/details/

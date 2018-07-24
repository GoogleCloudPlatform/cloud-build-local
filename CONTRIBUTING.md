## Contributor License Agreements

**This project is not yet set up to accept external contributions.**

## Contributor License Agreement

Before we can accept your pull requests you'll need to sign a Contributor
License Agreement (CLA):

*   If you are an individual writing original source code and you own the
    intellectual property, then you'll need to sign an
    [individual CLA](https://developers.google.com/open-source/cla/individual).

*   If you work for a company that wants to allow you to contribute your work,
    then you'll need to sign a
    [corporate CLA](https://developers.google.com/open-source/cla/corporate>).

You can sign these electronically (just scroll to the bottom). After that, we'll
be able to accept your pull requests.

## Developing the Local Builder

To build and test the Local Builder, you need a working
[Go environment](https://golang.org/doc/install). You should also install
[gcloud](https://cloud.google.com/sdk/docs/quickstarts) and
[Docker](https://www.docker.com/).

Run the following commands to install the Local Builder tool:

```
go get github.com/GoogleCloudPlatform/cloud-build-local
go install github.com/GoogleCloudPlatform/cloud-build-local
```

To run a build:

```
./bin/cloud-build-local --dryrun=false --config=path/to/cloudbuild.yaml path/to/code
```

To run the tests for Local Builder (without the vendored libraries):

```
go test $(go list github.com/GoogleCloudPlatform/cloud-build-local/... | grep -v vendor)
```

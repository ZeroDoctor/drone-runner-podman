# drone-runner-podman

The `podman` runner executes pipelines inside Podman containers. This runner is intended for linux workloads that are suitable for execution inside containers. This requires Drone server `1.6.0` or higher.

Documentation:<br/>
https://docs.drone.io/runner/podman/overview/

Technical Support:<br/>
https://discourse.drone.io

Issue Tracker and Roadmap:<br/>
https://trello.com/b/ttae5E5o/drone

## Release procedure

Run the changelog generator.

```BASH
podman run -it --rm -v "$(pwd)":/usr/local/src/your-app githubchangeloggenerator/github-changelog-generator -u drone-runners -p drone-runner-podman -t <secret github token>
```

You can generate a token by logging into your GitHub account and going to Settings -> Personal access tokens.

Next we tag the PR's with the fixes or enhancements labels. If the PR does not fufil the requirements, do not add a label.

**Before moving on make sure to update the version file `version/version.go && version/version_test.go`.**

Run the changelog generator again with the future version according to semver.

```BASH
podman run -it --rm -v "$(pwd)":/usr/local/src/your-app githubchangeloggenerator/github-changelog-generator -u drone-runners -p drone-runner-podman -t <secret token> --future-release v1.0.0
```

Create your pull request for the release. Get it merged then tag the release.


## Podman Binding Docs

https://github.com/containers/podman/tree/main/pkg/bindings

# Package and release the QQ adapter

[简体中文](releasing.zh-CN.md)

After [development verification](development.md), create a local package:

```fish
alonabot pack . --target linux/amd64 --out bin
```

The template release flow supports linux/amd64, linux/arm64, darwin/amd64, darwin/arm64 and windows/amd64. Archives contain the component declaration, executable, resources and `LICENSE`.

## Prepare a release

Update `plugin.yaml` and `changelog/<version>/{en-US,zh-CN}.md`. After review and maintainer merge, obtain release authorization before pushing the matching `v*` tag.

The [Release workflow](../.github/workflows/release.yml) resolves SDK `latest`, builds its matching CLI, runs tests/component checks and invokes:

```fish
bash scripts/release.sh v0.1.0 AlonaBot/alonabot-adapter-qq
```

The script checks version, source, bilingual changelogs and archived `LICENSE`, packages five platforms and generates bilingual release notes. The workflow publishes the archives as a GitHub Release. This implementation is delivered as a PR; maintainers control tags, merge and publication.

Release notes distinguish simulated verification from live QQ testing and retain actual account permission, channel capability and WebSocket availability restrictions.

# Release runbook

## Choosing the version

Pre-1.0, `0.x` treats **any** breaking change as a minor bump:

| Change | Bump |
|---|---|
| Renamed an env var, changed a default, changed an API field | `0.N+1.0` |
| New feature, backwards compatible | `0.N+1.0` |
| Bug fix only | `0.N.P+1` |

Renaming an env var that **has a default** is the dangerous one: consumers do not error, they silently revert to the default.

## Steps

1. **Bump on the feature branch**, so the PR and the release share one CI run:

   ```sh
   npm version 0.2.0 --no-git-tag-version   # the flag is required
   git commit -am 'Release 0.2.0'
   git push
   ```

2. **Open a PR against `main`.** Wait for the `test` job — `./devmail.sh check`, `go test -race`, the 25 MB size gate, and `./devmail.sh test` across both the dev and prod stacks — then merge.

3. **Wait for the `main` build to go green.** Merging starts a fresh run that publishes `edge` and `sha-<short>`. The tag must land after *this* run passes, not merely after the PR's did.

4. **Tag and push:**

   ```sh
   git checkout main && git pull
   node -p "require('./package.json').version"   # must equal the tag, minus the v
   git tag v0.2.0
   git push origin v0.2.0
   ```

> [!IMPORTANT]
> **Always pass `--no-git-tag-version`.** Despite the name it suppresses the *commit* as well as the tag, which is why the `git commit` above is separate. Without it, `npm version` creates both at once, so pushing sends the tag alongside the bump and it races ahead of the main build. If that run then fails, you own a published tag pointing at code that never passed — and a published tag cannot really be unpublished.

The `assert-version` job fails the run if the tag and `package.json` disagree.

## Release notes

Lead with anything that changes behaviour for someone who upgrades without reading a diff — `:latest` and `:0.1` users get the new image unannounced. Call out:

- renamed or re-defaulted environment variables;
- changes to the JSON API shape, since people script `/api/messages` in CI;
- anything newly written to logs or the network.

## If a release goes wrong

Roll forward with a new patch release rather than deleting and re-pushing a tag. Anyone who fetched it keeps it, and GHCR may hold partial artifacts.

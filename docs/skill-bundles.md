# First-class Skill packages

A Skill is a **reviewable bundle of actual local files**, not a capability label
or a block of instructions embedded in a Gateway event. The bundle ZIP contains:

- `manifest.json`: strict format-1 identity, integer version, short description,
  adapter declaration, and a list of relative paths with byte sizes and SHA-256.
- `SKILL.md`: nonempty UTF-8 documentation, at most 64 KiB.
- All explicitly declared reference, recipe, and asset files.

`cap.skill.<name>` identifies the Skill. Names use lowercase ASCII letters,
digits and underscores, start with a letter, and are at most 48 characters.
Bundles are at most 32 MiB compressed **and** expanded (including metadata),
with at most 256 content files. The 64 KiB manifest does not contain file bytes.
Scripts and ordinary assets may be carried for review but **never executed**.
No automatic dependency installation, shell, filesystem or network adapter exists.

## Two distinct operations

1. **Remote invocation** asks an already approved worker to run a callable Skill.
   The caller receives a bounded result; no package is installed on the caller.
2. **Package transfer** sends ZIP bytes through the existing outbound streaming
   relay. Receiving/staging is not approval, activation, execution or learning.
   The receiving operator reviews the files and approves an independently pinned
   archive SHA-256, capability and version locally.

A `manual` adapter stores ordinary documentation/assets for human use and is
announced with `callable:false`. It never appears in the worker's executable
capability IDs. A `text-recipe` adapter explicitly names a declared JSON recipe
using `internal/skills`: trim/lower/upper/prefix/suffix/replace only, bounded
4096-byte UTF-8 input/output, at most 16 operations and checked example cases.
The recipe's capability/version must match the bundle. Example tests are not a
proof that the package is trustworthy. Other adapters fail closed.
The legacy `cap.learned.text.*` recipe workflow remains available.

## Review and transfer example

```sh
# Teacher host: create a ZIP. Pack computes sizes/hashes from the listed files.
go run ./cmd/skillbundle -mode pack \
  -dir examples/skill-bundle-greeting -file greeting.zip

# Transport with cmd/transfer create/send and receive, as documented in
# streaming-relay.md. The receiver chooses a protected local download path.
# Do not send the ZIP contents or relay secrets in Gateway events.

# Learner host: stage inert bytes, then inspect the returned SHA256 and directory.
go run ./cmd/skillbundle -mode stage -root ./local-skills -file greeting.zip
go run ./cmd/skillbundle -mode inspect -root ./local-skills -sha256 "$SHA256"
# Read local-skills/staged/$SHA256/files/SKILL.md and every declared file.
# Compare the archive digest through a trusted channel before this local action:
go run ./cmd/skillbundle -mode approve -root ./local-skills \
  -sha256 "$SHA256" -capability cap.skill.greeting -version 1

# Works offline after teacher and Gateway are stopped.
go run ./cmd/skillbundle -mode execute -root ./local-skills \
  -capability cap.skill.greeting -text ' HELLO '
go run ./cmd/skillbundle -mode list -root ./local-skills

# Only a previously approved exact version/hash can be restored.
go run ./cmd/skillbundle -mode rollback -root ./local-skills \
  -capability cap.skill.greeting -version 1 -sha256 "$SHA256"
```

The source manifest under `examples/skill-bundle-greeting` is a packing template:
its file hashes/sizes are filled by `pack`. Wire manifests require exact hashes
and sizes. For a manual bundle use `"adapter":{"kind":"manual"}` and list the
files to carry. No approval flag exists in the manifest schema.

## Local state and limits

`approval.db` stores only local approvals and active-version references. Packages
cannot supply or replace this database. Versions are immutable: reusing a
capability/version with different bytes fails. Implicit downgrades fail; rollback
requires a previously approved version and matching digest. Failed validation
never changes the active version. Each inspection/announcement/invocation checks
the archive digest and every declared extracted file; corruption fails closed.
Archive path traversal, absolute paths, backslashes, control characters, symlinks,
duplicate ZIP entries/JSON keys, missing or undeclared files, hash/size mismatch,
unknown metadata, unsupported adapters and expanded-size bombs are rejected.

Protect the local store and **all its ancestor directories** from other users and
processes. This is not a sandbox against a malicious same-user local process.
Filesystem staging precedes transactional local approval; a crash can leave an
inert staged directory, never an imported approval. If local files are lost or
changed, an existing approval does not bypass revalidation. Inspect before retrying
any operation whose outcome was interrupted. ZIP bytes are buffered only on the
agents within the 32 MiB limit; Gateway relay keeps its bounded frame streaming.

Go tests include a real WebSocket relay transfer of a bundle containing a 30 MiB
asset, explicit approval, offline execution after Gateway shutdown, manual bundle
non-execution, rollback, immutable versions, tampering and malicious archives:

```sh
go test -race ./internal/skillbundle ./internal/skills
```

# First-class Skill packages

A Skill is a **reviewable bundle of actual local files**, not a capability label
or a block of instructions embedded in a Gateway event. For a first handoff,
start with the [acceptance checklist](#acceptance-checklist) and the
[greeting example](../examples/skill-bundle-greeting).

## Package the declared files

The format-1 ZIP contains a root `manifest.json` and exactly the files it
declares. Use the existing `cmd/skillbundle -mode pack` command from a local
source directory.

| Manifest field | What to put in it |
| --- | --- |
| `format` | The integer `1`. |
| `name` | A lowercase ASCII name beginning with a letter, followed by letters, digits or underscores, at most 48 characters. It maps to `cap.skill.<name>`. |
| `version` | An integer from 1 to 1,000,000. Use a new version when changing the package. |
| `description` | A short, nonempty UTF-8 description of at most 1024 bytes. |
| `adapter` | `{"kind":"manual"}` for reference material, or `{"kind":"text-recipe","recipe":"recipe.json"}` for a declared recipe file. |
| `files` | One entry per file, with a relative `path`. The packed manifest also has each file's byte `size` and lowercase hexadecimal `sha256`, both filled in by `pack`. |

Include a nonempty UTF-8 `SKILL.md` at the archive root, at most 64 KiB.
Describe what the Skill does, its input and output if callable, an example, and
any limitations reviewers should know. Its contents are documentation. The
runtime selects the adapter from the manifest and never runs instructions or
scripts from `SKILL.md`.

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

## Acceptance checklist

### 1. Pack and stage greeting

```sh
WORK=$(mktemp -d)
SKILL_STORE="$WORK/local-skills"
go run ./cmd/skillbundle -mode pack \
  -dir examples/skill-bundle-greeting -file "$WORK/greeting.zip"
go run ./cmd/skillbundle -mode stage -root "$SKILL_STORE" -file "$WORK/greeting.zip"
go run ./cmd/skillbundle -mode list -root "$SKILL_STORE"
```

Use a fresh, private test store. The staged inspection should say
`capability_id: cap.skill.greeting`, manifest version `1` and
`executable: true`. That flag describes the adapter. The active list should
still be empty. Before approval, the following command should return a failed
result and exit nonzero.

```sh
go run ./cmd/skillbundle -mode execute -root "$SKILL_STORE" \
  -capability cap.skill.greeting -text ' HELLO '
```

For a real handoff, transport the ZIP with `cmd/transfer`
[create/send/receive](streaming-relay.md) to a protected receiver-owned local
path, then run the same `stage` command on that file. Keep the ZIP contents and
relay secrets out of Gateway events.

### 2. Review and approve the exact package

Compare the staged ZIP's SHA-256 with the expected **archive** digest obtained
through a trusted channel. For this local example you can compare it with the
digest printed when you packed your own copy. Set `SHA256` to the reviewed
digest. Inspect the archive, read the extracted `SKILL.md` and every declared
file in `$SKILL_STORE/staged/$SHA256/files/`, then approve locally on the worker
host. Per-file digests validate the manifest contents. Approval pins the
combined capability, version and full ZIP digest.

```sh
go run ./cmd/skillbundle -mode inspect -root "$SKILL_STORE" -sha256 "$SHA256"
# Run approve only after comparing the digest and reviewing the files.
go run ./cmd/skillbundle -mode approve -root "$SKILL_STORE" \
  -capability cap.skill.greeting -version 1 -sha256 "$SHA256"
go run ./cmd/skillbundle -mode execute -root "$SKILL_STORE" \
  -capability cap.skill.greeting -text ' HELLO '
```

The last command runs offline. Expect `status: completed` and
`output.text: hello!`. The recipe trims the surrounding spaces, lowercases
the text, and appends `!`.

### 3. Check the worker contract

Set `GATEWAY_WS` to the WebSocket URL of an isolated loopback Gateway. After
approval, start the worker in a separate terminal with the same variables.

```sh
go run ./cmd/agent -mode worker -url "$GATEWAY_WS" -room skill-acceptance \
  -id greeting-worker -state "$WORK/worker.db" -skill-bundles "$SKILL_STORE"
```

In the [capability catalog](capability-catalog.md#skill-worker-contract),
expect `cap.skill.greeting` with `callable:true` and a descriptor matching the
approved digest and version. Then call the worker from your original terminal.

```sh
go run ./cmd/call -url "$GATEWAY_WS" -room skill-acceptance -id greeting-caller \
  -target greeting-worker -request-id greeting-acceptance-1 \
  -capability cap.skill.greeting -json '{"text":" HELLO "}'
```

The worker accepts one JSON field, `text`, containing a UTF-8 string of at
most 4096 bytes. The successful result is UTF-8 `text/plain` of at most
4096 bytes, in `output.text`. Use public test input because invocation
parameters and results are visible in room history.

As a negative check, create a source directory with a short, nonempty
`SKILL.md` and this `manifest.json`. Pack, stage, review and locally approve it
with its own ZIP digest and identity `cap.skill.greeting_notes`, version `1`.

```json
{
  "format": 1,
  "name": "greeting_notes",
  "version": 1,
  "description": "Greeting notes for human review",
  "adapter": {"kind": "manual"},
  "files": [{"path": "SKILL.md"}]
}
```

The approved manual package can appear in the catalog with `callable:false`.
Start a test worker with that store, then repeat the call with
`-capability cap.skill.greeting_notes` and a fresh request ID. Once that
announcement is visible, the Gateway rejects the call as
`advertised capability is not callable`. Running the local `-mode execute`
command for the same capability exits nonzero with
`Skill is manual, not executable`. The package's `SKILL.md` is for human use.

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

Restore a previously approved exact version with `cmd/skillbundle -mode rollback`
using the same `-root`, `-capability`, `-version` and `-sha256` arguments as
approval.

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

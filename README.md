# muxtail

Tail multiple files simultaneously, with optional labeled and colored output.

It's similar to `tail` and `multitail`, but supports line prefixing and does not require a tty.

## Usage

```
muxtail [flags] [FILE ...]
```

Pass `-` or omit files to read from stdin.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--lines` | `-n` | `10` | Initial lines to show per file |
| `--follow` | `-f` | off | Follow files for new lines |
| `--follow-retry` | `-F` | off | Follow and retry if file is missing/recreated |
| `--prefix` | `-p` | `none` | Auto-label mode: `none`, `basename`, `fullname` |
| `--label` | `-l` | — | Per-file label (repeatable, positional) |
| `--ts` | `-T` | off | Prepend each line with a timestamp |
| `--no-color` | — | off | Disable colored labels |

## Examples

**Tail two files with auto labels:**
```
muxtail -p basename app.log db.log
```
```
app.log: line 1
db.log: line 1
app.log: line 2
db.log: line 2
```

**Follow with custom labels:**
```
muxtail -f -l "[api] " -l "[db] " app.log db.log
```
```
[api] starting server on :8080
[db] connected to postgres
[api] GET /health 200
[db] query completed in 3ms
```

**Timestamps + follow:**
```
muxtail -Tf app.log
```
```
2024-11-01T12:00:01 starting server on :8080
2024-11-01T12:00:02 GET /health 200
2024-11-01T12:00:05 POST /users 201
```

**Disable colors (e.g. when piping):**
```
muxtail --no-color -p basename app.log db.log | grep ERROR
```
```
app.log: ERROR connection refused
db.log: ERROR timeout after 30s
```
Colors are also suppressed automatically when stdout is not a terminal.

**Tail stdin:**
```
kubectl logs -f my-pod | muxtail --ts -l "[pod] "
```
```
2024-11-01T12:00:01 [pod] starting up
2024-11-01T12:00:02 [pod] ready to serve traffic
```

## Prefix modes

`--prefix` / `-p` controls how files are auto-labeled:

| Mode | Label for `/var/log/app.log` | Description |
|------|------------------------------|-------------|
| `none` (default) | _(no label)_ | Lines are written with no prefix |
| `basename` | `app.log: ` | Uses the filename without the directory path |
| `fullname` | `/var/log/app.log: ` | Uses the full path as given on the command line |

For stdin (`-`), both `basename` and `fullname` produce `stdin: `.

`--label` overrides `--prefix` on a per-file basis (matched positionally). Unlabeled files fall back to the `--prefix` mode.

## Label convention

Labels are written verbatim before each line — include your own spacing and brackets (e.g. `"[db] "`). `--label` takes priority over `--prefix` for each positional file.

## Install

```
go install muxtail@latest
```

Or build from source:

```
go build -o muxtail .
```

## Notes

**This project has been developed with AI assistance.**
